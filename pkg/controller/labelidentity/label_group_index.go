// Copyright 2022 Antrea Authors
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

package labelidentity

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"antrea.io/antrea/pkg/controller/types"
)

const (
	// Cluster scoped selectors are stored under empty Namespace in indice.
	emptyNamespace = ""
	policyIndex    = "policyIndex"
)

type selectorItem struct {
	selector          types.GroupSelector
	labelIdentityKeys sets.String
	policyKeys        sets.String
}

type labelIdentityMatch struct {
	id               uint32
	namespace        string
	namespaceLabels  map[string]string
	podLabels        map[string]string
	selectorItemKeys sets.String
}

func (l *labelIdentityMatch) matches(s *selectorItem) bool {
	selectorItemNamespace := s.selector.Namespace
	if selectorItemNamespace != "" {
		if selectorItemNamespace != l.namespace {
			return false
		}
	} else if s.selector.NamespaceSelector != nil && !s.selector.NamespaceSelector.Matches(labels.Set(l.namespaceLabels)) {
		return false
	}
	// At this stage Namespace has matched
	if s.selector.PodSelector != nil {
		return s.selector.PodSelector.Matches(labels.Set(l.podLabels))
	}
	// SelectorItem selects nothing when all selectors are missing.
	return false
}

func constructMapFromLabelString(s string) map[string]string {
	m := map[string]string{}
	kvs := strings.Split(s, ",")
	for _, kv := range kvs {
		kvpair := strings.Split(kv, "=")
		m[kvpair[0]] = kvpair[1]
	}
	return m
}

func newLabelIdentityMatch(labelIdentity string, id uint32) *labelIdentityMatch {
	r := regexp.MustCompile(`namespace:(?P<nslabels>(.)*),pod:(?P<podlabels>(.)*)`)
	nsIndex, podIndex := r.SubexpIndex("nslabels"), r.SubexpIndex("podlabels")

	labelMatches := r.FindStringSubmatch(labelIdentity)
	nsLabels := constructMapFromLabelString(labelMatches[nsIndex])
	podLabels := constructMapFromLabelString(labelMatches[podIndex])

	namespace, ok := nsLabels[apiv1.LabelMetadataName]
	if !ok {
		klog.Errorf("failed to construct labelIdenityMatch for labelIdentity %s: no namespace name label", labelIdentity)
		return nil
	}
	return &labelIdentityMatch{
		id:               id,
		namespace:        namespace,
		namespaceLabels:  nsLabels,
		podLabels:        podLabels,
		selectorItemKeys: sets.NewString(),
	}
}

// selectorItemKeyFunc knows how to get the key of a selectorItem.
func selectorItemKeyFunc(obj interface{}) (string, error) {
	sItem, ok := obj.(*selectorItem)
	if !ok {
		return "", fmt.Errorf("object is not of type *selectorItem: %v", obj)
	}
	return sItem.selector.NormalizedName, nil
}

func newSelectorItemStore() cache.Indexer {
	indexers := cache.Indexers{
		cache.NamespaceIndex: func(obj interface{}) ([]string, error) {
			sItem, ok := obj.(*selectorItem)
			if !ok {
				return []string{}, nil
			}
			// sItem.Selector.Namespace == "" means it's a cluster scoped selector, we index it as it is.
			return []string{sItem.selector.Namespace}, nil
		},
		policyIndex: func(obj interface{}) ([]string, error) {
			sItem, ok := obj.(*selectorItem)
			if !ok {
				return []string{}, nil
			}
			return sItem.policyKeys.List(), nil
		},
	}
	return cache.NewIndexer(selectorItemKeyFunc, indexers)
}

type LabelIdentityIndex struct {
	lock sync.RWMutex

	labelIdentities map[string]*labelIdentityMatch

	labelIdentityNamespaceIndex map[string]sets.String

	selectorItems cache.Indexer

	//selectorItemNamespaceIndex map[string]sets.String
}

func NewLabelIdentityIndex() *LabelIdentityIndex {
	index := &LabelIdentityIndex{
		labelIdentities: map[string]*labelIdentityMatch{},
		labelIdentityNamespaceIndex: map[string]sets.String{},
		selectorItems: newSelectorItemStore(),
	}
	return index
}

func (i *LabelIdentityIndex) updateSelector(selector types.GroupSelector, policyKey string) []uint32 {
	i.lock.Lock()
	defer i.lock.Unlock()

	selectorKey, selectorNS := selector.NormalizedName, selector.Namespace
	if s, exists, _ := i.selectorItems.GetByKey(selectorKey); exists {
		sItem := s.(*selectorItem)
		sItem.policyKeys.Insert(policyKey)
		return i.getMatchedLabelIdentityIDs(sItem)
	}
	sItem := &selectorItem{
		selector:          selector,
		labelIdentityKeys: sets.NewString(),
		policyKeys:        sets.NewString(policyKey),
	}
	i.selectorItems.Add(sItem)
	if selectorNS != "" {
		labelIdentityKeys, _ := i.labelIdentityNamespaceIndex[selectorNS]
		i.scanLabelIdentityMatches(labelIdentityKeys, sItem)
	} else {
		for _, labelIdentityKeys := range i.labelIdentityNamespaceIndex {
			i.scanLabelIdentityMatches(labelIdentityKeys, sItem)
		}
	}
	return i.getMatchedLabelIdentityIDs(sItem)
}

func (i *LabelIdentityIndex) getMatchedLabelIdentityIDs(sItem *selectorItem) []uint32 {
	var ids []uint32
	for lKey := range sItem.labelIdentityKeys {
		labelIdentity := i.labelIdentities[lKey]
		ids = append(ids, labelIdentity.id)
	}
	return ids
}

func (i *LabelIdentityIndex) scanLabelIdentityMatches(labelIdentityKeys sets.String, sItem *selectorItem) {
	for lkey := range labelIdentityKeys {
		labelIdentity := i.labelIdentities[lkey]
		if labelIdentity.matches(sItem) {
			// TODO: if does not care about update, can just blindly insert
			if !sItem.labelIdentityKeys.Has(lkey) {
				sItem.labelIdentityKeys.Insert(lkey)
				labelIdentity.selectorItemKeys.Insert(sItem.selector.NormalizedName)
			}
		}
	}
}
