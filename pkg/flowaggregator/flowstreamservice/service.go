// Copyright 2026 Antrea Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package flowstreamservice implements the FlowStreamService gRPC server-streaming
// RPC. It reads flow records from the Flow Aggregator's ring buffer and streams
// them to connected clients with server-side filtering.
package flowstreamservice

import (
	"net/netip"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	flowpb "antrea.io/antrea/v2/pkg/apis/flow/v1alpha1"
	"antrea.io/antrea/v2/pkg/flowaggregator/ringbuffer"
)

const (
	internalBatchSize = 100
	// contextCheckInterval caps how long ConsumeMultiple blocks before returning
	// with n=0 so that the GetFlows loop can check ctx.Err() and exit promptly
	// when the client disconnects. This does not add latency to flow delivery —
	// ConsumeMultiple returns immediately when flows are available.
	contextCheckInterval = 100 * time.Millisecond
)

// FlowStreamService implements flowpb.FlowStreamServiceServer. Each client
// connection gets its own independent ring-buffer Consumer, so clients are
// fully decoupled and a slow client never stalls faster ones.
type FlowStreamService struct {
	flowpb.UnimplementedFlowStreamServiceServer
	buffer ringbuffer.BroadcastBuffer[*flowpb.Flow]
}

// NewFlowStreamService creates a FlowStreamService backed by the given buffer.
func NewFlowStreamService(buffer ringbuffer.BroadcastBuffer[*flowpb.Flow]) *FlowStreamService {
	return &FlowStreamService{buffer: buffer}
}

// GetFlows is the server-streaming RPC handler. The gRPC framework spawns a
// goroutine per connected client and calls this method. It exits when:
//   - the client disconnects (ctx cancelled / stream.Send error),
//   - follow=false and all historical flows have been sent,
//   - max_count flows have been sent,
//   - or the ring buffer shuts down.
func (s *FlowStreamService) GetFlows(req *flowpb.GetFlowsRequest, stream flowpb.FlowStreamService_GetFlowsServer) error {
	ctx := stream.Context()

	var since time.Time
	if ts := req.GetSince(); ts != nil {
		since = ts.AsTime()
	}
	maxCount := int(req.GetMaxCount())
	follow := req.GetFollow()

	var labelSel labels.Selector
	if selStr := req.GetFilter().GetPodLabelSelector(); selStr != "" {
		var err error
		labelSel, err = labels.Parse(selStr)
		if err != nil {
			return status.Errorf(codes.InvalidArgument, "invalid pod_label_selector %q: %v", selStr, err)
		}
	}

	klog.InfoS("Client connected to FlowStreamService",
		"follow", follow,
		"since", since,
		"maxCount", maxCount,
		"filter", req.GetFilter())

	consumer := s.buffer.NewConsumer(
		ringbuffer.WithReadFromBeginning(),
		ringbuffer.WithMaxConsumeDeadline(contextCheckInterval),
	)

	sent := 0
	var totalDropped int64
	batch := make([]*flowpb.Flow, internalBatchSize)

	for {
		if err := ctx.Err(); err != nil {
			klog.V(4).InfoS("Client context done, stopping GetFlows", "err", err)
			return status.FromContextError(err).Err()
		}

		n, dropped, shutdown := consumer.ConsumeMultiple(batch)
		totalDropped += dropped

		if n > 0 {
			filtered := applyFilter(batch[:n], req.GetFilter(), labelSel, since)

			if maxCount > 0 && sent+len(filtered) >= maxCount {
				filtered = filtered[:maxCount-sent]
			}

			if len(filtered) > 0 || dropped > 0 {
				resp := &flowpb.GetFlowsResponse{
					Flows:        filtered,
					DroppedCount: uint64(totalDropped),
				}
				if err := stream.Send(resp); err != nil {
					klog.V(4).InfoS("Send to client failed, stopping GetFlows", "err", err)
					return err
				}
				sent += len(filtered)
			}

			if maxCount > 0 && sent >= maxCount {
				klog.V(4).InfoS("Reached max_count, closing GetFlows stream", "sent", sent)
				return nil
			}
		}

		if shutdown {
			klog.V(4).InfoS("Ring buffer shut down, closing GetFlows stream")
			return nil
		}

		if !follow && n == 0 {
			klog.V(4).InfoS("Caught up to ring buffer tail, closing non-follow stream")
			return nil
		}
	}
}

func applyFilter(flows []*flowpb.Flow, filter *flowpb.FlowFilter, labelSel labels.Selector, since time.Time) []*flowpb.Flow {
	filtered := flows[:0]
	for _, f := range flows {
		if !since.IsZero() && f.GetEndTs() != nil && f.GetEndTs().AsTime().Before(since) {
			continue
		}
		if filter != nil && !matchFilter(f, filter, labelSel) {
			continue
		}
		filtered = append(filtered, f)
	}
	return filtered
}

func matchFilter(f *flowpb.Flow, filter *flowpb.FlowFilter, labelSel labels.Selector) bool {
	k8s := f.GetK8S()
	direction := filter.GetDirection()

	if len(filter.GetNamespaces()) > 0 {
		if !matchNamespace(k8s, filter.GetNamespaces(), direction) {
			return false
		}
	}
	if len(filter.GetPodNames()) > 0 {
		if !matchPodNames(k8s, filter.GetPodNames(), direction) {
			return false
		}
	}
	if labelSel != nil {
		if !matchLabelSelector(k8s, labelSel, direction) {
			return false
		}
	}
	if len(filter.GetFlowTypes()) > 0 {
		if k8s == nil || !containsFlowType(filter.GetFlowTypes(), k8s.GetFlowType()) {
			return false
		}
	}
	if len(filter.GetServiceNames()) > 0 {
		if k8s == nil {
			return false
		}
		svcName := k8s.GetDestinationServicePortName()
		if !slices.ContainsFunc(filter.GetServiceNames(), func(input string) bool {
			return serviceFilterMatches(svcName, input)
		}) {
			return false
		}
	}
	if len(filter.GetIps()) > 0 {
		if !matchIPs(f, filter.GetIps(), direction) {
			return false
		}
	}
	return true
}

func matchLabelSelector(k8s *flowpb.Kubernetes, sel labels.Selector, direction flowpb.FlowFilterDirection) bool {
	if k8s == nil {
		return false
	}
	checkSrc := direction != flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_TO
	checkDst := direction != flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM
	if checkSrc {
		if sel.Matches(labelsFromProto(k8s.GetSourcePodLabels())) {
			return true
		}
	}
	if checkDst {
		if sel.Matches(labelsFromProto(k8s.GetDestinationPodLabels())) {
			return true
		}
	}
	return false
}

func labelsFromProto(l *flowpb.Labels) labels.Set {
	if l == nil {
		return labels.Set{}
	}
	return labels.Set(l.GetLabels())
}

func matchNamespace(k8s *flowpb.Kubernetes, namespaces []string, direction flowpb.FlowFilterDirection) bool {
	if k8s == nil {
		return false
	}
	for _, ns := range namespaces {
		switch direction {
		case flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM:
			if k8s.GetSourcePodNamespace() == ns {
				return true
			}
		case flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_TO:
			if k8s.GetDestinationPodNamespace() == ns {
				return true
			}
		default:
			if k8s.GetSourcePodNamespace() == ns || k8s.GetDestinationPodNamespace() == ns {
				return true
			}
		}
	}
	return false
}

func matchPodNames(k8s *flowpb.Kubernetes, podNames []string, direction flowpb.FlowFilterDirection) bool {
	if k8s == nil {
		return false
	}
	for _, name := range podNames {
		switch direction {
		case flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM:
			if k8s.GetSourcePodName() == name {
				return true
			}
		case flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_TO:
			if k8s.GetDestinationPodName() == name {
				return true
			}
		default:
			if k8s.GetSourcePodName() == name || k8s.GetDestinationPodName() == name {
				return true
			}
		}
	}
	return false
}

func matchIPs(f *flowpb.Flow, ips []string, direction flowpb.FlowFilterDirection) bool {
	ip := f.GetIp()
	if ip == nil {
		return false
	}
	srcAddr, srcOK := netip.AddrFromSlice(ip.GetSource())
	dstAddr, dstOK := netip.AddrFromSlice(ip.GetDestination())

	checkSrc := direction != flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_TO
	checkDst := direction != flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM

	for _, ipStr := range ips {
		if strings.Contains(ipStr, "/") {
			prefix, err := netip.ParsePrefix(ipStr)
			if err != nil {
				continue
			}
			if checkSrc && srcOK && prefix.Contains(srcAddr) {
				return true
			}
			if checkDst && dstOK && prefix.Contains(dstAddr) {
				return true
			}
		} else {
			addr, err := netip.ParseAddr(ipStr)
			if err != nil {
				continue
			}
			if checkSrc && srcOK && srcAddr == addr {
				return true
			}
			if checkDst && dstOK && dstAddr == addr {
				return true
			}
		}
	}
	return false
}

func containsFlowType(types []flowpb.FlowType, t flowpb.FlowType) bool {
	for _, ft := range types {
		if ft == t {
			return true
		}
	}
	return false
}

// destinationServiceFilterKey returns the namespace/service identity with any
// trailing ":port" or ":portName" removed from kube-proxy ServicePortName
// formatting ("namespace/name:port").
func destinationServiceFilterKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Only handle valid namespace/service format
	i := strings.Index(s, "/")
	if i <= 0 {
		return ""
	}
	if j := strings.Index(s[i:], ":"); j >= 0 {
		return s[:i+j]
	}
	return s
}

// serviceFilterMatches reports whether filterToken selects the flow's destination
// service. Both flowDestinationServicePortName and filterToken are expected to be
// in namespace/service format (with optional port suffix that gets stripped).
func serviceFilterMatches(flowDestinationServicePortName, filterToken string) bool {
	flowKey := destinationServiceFilterKey(flowDestinationServicePortName)
	if flowKey == "" {
		return false
	}
	want := strings.TrimSpace(filterToken)
	if want == "" {
		return false
	}
	wantKey := destinationServiceFilterKey(want)
	return wantKey == flowKey
}
