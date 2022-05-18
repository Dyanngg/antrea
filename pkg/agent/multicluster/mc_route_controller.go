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

package noderoute

import (
	"fmt"
	"net"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	mcv1alpha1 "antrea.io/antrea/multicluster/apis/multicluster/v1alpha1"
	mcclientset "antrea.io/antrea/multicluster/pkg/client/clientset/versioned"
	mcinformers "antrea.io/antrea/multicluster/pkg/client/informers/externalversions/multicluster/v1alpha1"
	mclisters "antrea.io/antrea/multicluster/pkg/client/listers/multicluster/v1alpha1"
	"antrea.io/antrea/pkg/agent/config"
	"antrea.io/antrea/pkg/agent/interfacestore"
	"antrea.io/antrea/pkg/agent/openflow"
	"antrea.io/antrea/pkg/ovs/ovsconfig"
)

const (
	controllerName = "AntreaAgentMCRouteController"

	// Set resyncPeriod to 0 to disable resyncing
	resyncPeriod = 0 * time.Second
	// How long to wait before retrying the processing of a resource change
	minRetryDelay = 2 * time.Second
	maxRetryDelay = 120 * time.Second

	// Default number of workers processing a resource change
	defaultWorkers = 1
)

type key struct {
	kind string
	name string
}

type ciImportInfo struct {
	localGWName string
	ciImport    mcv1alpha1.ClusterInfoImport
}

// MCRouteController watches Gateway and ClusterInfoImport events.
// It is responsible for setting up necessary Openflow entries for multi-cluster
// traffic on a Gateway Node.
type MCRouteController struct {
	mcClient             mcclientset.Interface
	ovsBridgeClient      ovsconfig.OVSBridgeClient
	ofClient             openflow.Client
	interfaceStore       interfacestore.InterfaceStore
	nodeConfig           *config.NodeConfig
	gwInformer           mcinformers.GatewayInformer
	gwLister             mclisters.GatewayLister
	gwListerSynced       cache.InformerSynced
	ciImportInformer     mcinformers.ClusterInfoImportInformer
	ciImportLister       mclisters.ClusterInfoImportLister
	ciImportListerSynced cache.InformerSynced
	queue                workqueue.RateLimitingInterface
	installedCIImport    cache.Indexer
	installedGWName      string
	namespace            string
}

func NewMCRouteController(
	mcClient mcclientset.Interface,
	gwInformer mcinformers.GatewayInformer,
	ciImportInformer mcinformers.ClusterInfoImportInformer,
	client openflow.Client,
	ovsBridgeClient ovsconfig.OVSBridgeClient,
	interfaceStore interfacestore.InterfaceStore,
	nodeConfig *config.NodeConfig,
	namespace string,
) *MCRouteController {
	controller := &MCRouteController{
		mcClient:             mcClient,
		ovsBridgeClient:      ovsBridgeClient,
		ofClient:             client,
		interfaceStore:       interfaceStore,
		nodeConfig:           nodeConfig,
		gwInformer:           gwInformer,
		gwLister:             gwInformer.Lister(),
		gwListerSynced:       gwInformer.Informer().HasSynced,
		ciImportInformer:     ciImportInformer,
		ciImportLister:       ciImportInformer.Lister(),
		ciImportListerSynced: ciImportInformer.Informer().HasSynced,
		queue:                workqueue.NewNamedRateLimitingQueue(workqueue.NewItemExponentialFailureRateLimiter(minRetryDelay, maxRetryDelay), "gatewayroute"),
		installedCIImport:    cache.NewIndexer(clusterInfoImportKeyFunc, cache.Indexers{}),
		namespace:            namespace,
	}
	controller.gwInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(cur interface{}) {
				controller.enqueueGateway(cur, false)
			},
			UpdateFunc: func(old, cur interface{}) {
				controller.enqueueGateway(cur, false)
			},
			DeleteFunc: func(old interface{}) {
				controller.enqueueGateway(old, true)
			},
		},
		resyncPeriod,
	)
	controller.ciImportInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(cur interface{}) {
				controller.enqueueClusterInfoImport(cur)
			},
			UpdateFunc: func(old, cur interface{}) {
				controller.enqueueClusterInfoImport(cur)
			},
			DeleteFunc: func(old interface{}) {
				controller.enqueueClusterInfoImport(old)
			},
		},
		resyncPeriod,
	)
	return controller
}

func clusterInfoImportKeyFunc(obj interface{}) (string, error) {
	return obj.(*ciImportInfo).ciImport.Name, nil
}

func (c *MCRouteController) enqueueGateway(obj interface{}, isDelete bool) {
	gw, isGW := obj.(*mcv1alpha1.Gateway)
	if !isGW {
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.Errorf("received unexpected object: %v", obj)
			return
		}
		gw, ok = deletedState.Obj.(*mcv1alpha1.Gateway)
		if !ok {
			klog.Errorf("DeletedFinalStateUnknown contains non-Gateway object: %v", deletedState.Obj)
			return
		}
	}
	if !isDelete {
		if net.ParseIP(gw.InternalIP) == nil || net.ParseIP(gw.GatewayIP) == nil {
			klog.Errorf("no valid Internal IP or Gateway IP is found in Gateway %s", gw.Namespace+"/"+gw.Name)
			return
		}
	}
	c.queue.Add(key{
		kind: "Gateway",
		name: gw.Name,
	})
}

func (c *MCRouteController) enqueueClusterInfoImport(obj interface{}) {
	ciImp, isciImp := obj.(*mcv1alpha1.ClusterInfoImport)
	if !isciImp {
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.Errorf("received unexpected object: %v", obj)
			return
		}
		ciImp, ok = deletedState.Obj.(*mcv1alpha1.ClusterInfoImport)
		if !ok {
			klog.Errorf("DeletedFinalStateUnknown contains non-ClusterInfoImport object: %v", deletedState.Obj)
			return
		}
	}

	c.queue.Add(key{
		kind: "ClusterInfoImport",
		name: ciImp.Name,
	})
}

// Run will create defaultWorkers workers (go routines) which will process
// the Gateway events from the workqueue.
func (c *MCRouteController) Run(stopCh <-chan struct{}) {
	defer c.queue.ShutDown()
	cacheSyncs := []cache.InformerSynced{c.gwListerSynced, c.ciImportListerSynced}
	klog.Infof("Starting %s", controllerName)
	defer klog.Infof("Shutting down %s", controllerName)
	if !cache.WaitForNamedCacheSync(controllerName, stopCh, cacheSyncs...) {
		return
	}

	for i := 0; i < defaultWorkers; i++ {
		go wait.Until(c.worker, time.Second, stopCh)
	}
	<-stopCh
}

// worker is a long-running function that will continually call the processNextWorkItem
// function in order to read and process a message on the workqueue.
func (c *MCRouteController) worker() {
	for c.processNextWorkItem() {
	}
}

func (c *MCRouteController) processNextWorkItem() bool {
	obj, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(obj)

	if k, ok := obj.(key); !ok {
		c.queue.Forget(obj)
		klog.InfoS("Expected type 'key' in work queue but got object", "object", obj)
		return true
	} else if err := c.syncMCFlows(k); err == nil {
		c.queue.Forget(k)
	} else {
		// Put the item back on the workqueue to handle any transient errors.
		c.queue.AddRateLimited(k)
		klog.ErrorS(err, "Error syncing key, requeuing", "key", k)
	}
	return true
}

func (c *MCRouteController) syncMCFlows(k key) error {
	startTime := time.Now()
	defer func() {
		klog.V(4).InfoS("Finished syncing Gateway Node flows for Multi-cluster", "type", k.kind,
			"name", k.name, "time", time.Since(startTime))
	}()
	switch k.kind {
	case "Gateway":
		klog.V(2).InfoS("Processing for Gateway", "gateway", k.name)
		return c.handleGatewayEvents(k)
	case "ClusterInfoImport":
		klog.V(2).InfoS("Processing for ClusterInfoImport", "clusterinfoimport", k.name)
		return c.handleCIImportEvents(k)
	}
	return nil
}

func (c *MCRouteController) handleGatewayEvents(k key) error {
	var gwIsActive, iAmActiveGW, noActiveGateway bool
	activeGW, iamGW, getGWErr := c.checkActiveGateway()
	klog.V(2).InfoS("Got the active Gateway", "gateway", klog.KObj(activeGW))
	if getGWErr != nil {
		if strings.Contains(getGWErr.Error(), "no Gateway found") {
			noActiveGateway = true
			iAmActiveGW = false
			iamGW = c.nodeConfig.Name == k.name
		}
	} else {
		gwIsActive = activeGW.Name == k.name
		iAmActiveGW = activeGW.Name == c.nodeConfig.Name
	}

	_, err := c.gwLister.Gateways(c.namespace).Get(k.name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := c.cleanupFlows(k, iamGW); err != nil {
				return err
			}
			// When there is no active Gateway after a Gateway deletion, just return.
			// Otherwise, we need reinstall multi-cluster flows for the latest active Gateway
			if getGWErr != nil {
				if noActiveGateway {
					return nil
				}
				return err
			}
			klog.V(2).InfoS("Installing multi-cluster flows for the active Gateway", "gateway", klog.KObj(activeGW))
			if !iAmActiveGW {
				return c.installFlowsOnRegularNode(activeGW)
			}
			return c.installFlowsOnGateway(activeGW)
		}
		return err
	}

	if getGWErr != nil {
		return getGWErr
	}

	if iAmActiveGW && (activeGW.Name != c.installedGWName && c.installedGWName != "") {
		// Clean up previous cross-cluster flows for regular Node when the Node becomes an active Gateway.
		// or this new active Gateway is different with cached Gateway.
		klog.V(2).InfoS("Clean up multi-cluster flows for installed Gateway", "gateway", c.installedGWName)
		if err := c.cleanupFlows(key{kind: "Gateway", name: c.installedGWName}, false); err != nil {
			return err
		}
	}

	if iAmActiveGW {
		// Install Gateway flows only when the Node is the active Gateway.
		return c.installFlowsOnGateway(activeGW)
	}
	if iamGW {
		// Clean up previous multi-cluster flows for this Node when it becomes inactive Gateway.
		klog.V(2).InfoS("Clean up multi-cluster flows when the Node becomes inactive Gateway", "node", c.nodeConfig.Name)
		if err := c.cleanupFlows(key{kind: "Gateway", name: c.nodeConfig.Name}, true); err != nil {
			return err
		}
	} else {
		if gwIsActive {
			// Clean up previous multi-cluster flows for this Node.
			if c.installedGWName != "" {
				klog.V(2).InfoS("Clean up multi-cluster flows for installed Gateway", "gateway", c.installedGWName)
				if err := c.cleanupFlows(key{kind: "Gateway", name: c.installedGWName}, false); err != nil {
					return err
				}
			}
		}
	}
	// When the Node is not active Gateway, and coming Gateway is the active one,
	// install multi-cluster flows as regular Node.
	klog.V(2).InfoS("Installing multi-cluster flows for the Gateway", "gateway", klog.KObj(activeGW))
	return c.installFlowsOnRegularNode(activeGW)
}

func (c *MCRouteController) cleanupFlows(k key, flowsOnGW bool) error {
	klog.InfoS("Deleting remote Node flows for multi-cluster traffic", "kind", k.kind, "name", k.name)
	var prefix, gwName string
	if flowsOnGW {
		prefix = "gw-"
	} else {
		gwName = k.name
	}
	if err := c.deleteMCNodeFlowsForGatewayEvents(gwName); err != nil {
		return err
	}
	if err := c.ofClient.UninstallMulticlusterFlows(prefix + k.name); err != nil {
		return err
	}
	klog.V(2).InfoS("Deleted flows for Gateway", "gateway", k.name)
	return nil
}

func (c *MCRouteController) installFlowsOnGateway(gw *mcv1alpha1.Gateway) error {
	// Install Gateway flows only when the Node is the active Gateway.
	if err := c.ofClient.InstallMulticlusterClassifierFlows("gw-"+gw.Name, config.DefaultTunOFPort, true); err != nil {
		return err
	}
	return c.addMCNodeFlows(gw, nil, true)
}

func (c *MCRouteController) installFlowsOnRegularNode(gw *mcv1alpha1.Gateway) error {
	if err := c.ofClient.InstallMulticlusterClassifierFlows(gw.Name, config.DefaultTunOFPort, false); err != nil {
		return err
	}
	return c.addMCNodeFlows(gw, nil, false)
}

func (c *MCRouteController) handleCIImportEvents(k key) error {
	gw, _, err := c.checkActiveGateway()
	if err != nil {
		if strings.Contains(err.Error(), "no Gateway found") {
			// No need to handle not found error since multi-cluster flows will be added
			// when a Gateway is created.
			klog.V(2).InfoS("No valid Gateway is found", "err", err)
			return nil
		}
		return err
	}
	iAmActiveGW := gw.Name == c.nodeConfig.Name
	ciImp, err := c.ciImportLister.ClusterInfoImports(c.namespace).Get(k.name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			cachedCI, isExist, _ := c.installedCIImport.GetByKey(k.name)
			if isExist {
				cachedata := cachedCI.(*ciImportInfo)
				klog.InfoS("Deleting remote Node flows for multi-cluster traffic", "kind", k.kind, "name", k.name)
				if !iAmActiveGW {
					return c.deleteMCNodeFlowsForCIImportEvents(&cachedata.ciImport,
						getCacheKeyWithPrefix(gw.Name+cachedata.ciImport.Spec.ClusterID))
				}
				return c.deleteMCNodeFlowsForCIImportEvents(&cachedata.ciImport,
					getCacheKeyWithPrefix(cachedata.ciImport.Spec.ClusterID))
			}
			klog.V(2).InfoS("No cached ClusterInfoImport found, do nothing")
			return nil
		}
		return err
	}
	klog.V(2).InfoS("Add multic-cluster flows for ClusterInfoImport", "clusterinfoimport", ciImp.Name)
	if err = c.addMCNodeFlows(gw, ciImp, iAmActiveGW); err != nil {
		return err
	}
	return nil
}

func (c *MCRouteController) addMCNodeFlows(gw *mcv1alpha1.Gateway, ciImp *mcv1alpha1.ClusterInfoImport, iAmActiveGW bool) error {
	allciImports, err := c.getAllCIImports(ciImp)
	if allciImports == nil {
		return err
	}
	var tunnelPeerIP net.IP
	for _, ciImport := range allciImports {
		tunnelPeerIP = getPeerGatewayIP(ciImport.Spec)
		if tunnelPeerIP == nil {
			klog.V(2).InfoS("No valid peer Gateway IP from ClusterInfoImport, skip updating openflow rules",
				"clusterinfoimport", klog.KObj(ciImport), "node", c.nodeConfig.Name)
			continue
		}

		klog.InfoS("Adding/Updating remote Gateway Node flows for Multi-cluster", "gateway", klog.KObj(gw),
			"node", c.nodeConfig.Name, "peer", tunnelPeerIP)
		allCIDRs := []string{ciImport.Spec.ServiceCIDR}
		peerConfigs, err := generatePeerConfigs(allCIDRs, tunnelPeerIP)
		if err != nil {
			klog.ErrorS(err, "Parse error for serviceCIDR from remote cluster", "clusterinfoimport", ciImport.Name, "gateway", gw.Name)
			return err
		}
		if iAmActiveGW {
			klog.InfoS("Adding/Updating flows to remote Gateway Node for Multi-cluster traffic", "clusterinfoimport", ciImport.Name, "cidrs", allCIDRs)
			localGatewayIP := net.ParseIP(gw.GatewayIP)
			if err := c.ofClient.InstallMulticlusterGatewayFlows(
				getCacheKeyWithPrefix(ciImport.Spec.ClusterID),
				peerConfigs,
				tunnelPeerIP,
				localGatewayIP); err != nil {
				return fmt.Errorf("failed to install flows to remote Gateway in ClusterInfoImport %s: %v", ciImport.Name, err)
			}
		} else {
			tunnelPeerIP := net.ParseIP(gw.InternalIP)
			if err := c.ofClient.InstallMulticlusterNodeFlows(
				getCacheKeyWithPrefix(gw.Name+ciImport.Spec.ClusterID),
				peerConfigs,
				tunnelPeerIP); err != nil {
				return fmt.Errorf("failed to install flows to Gateway %s: %v", gw.Name, err)
			}
		}

		c.installedCIImport.Add(&ciImportInfo{
			localGWName: gw.Name,
			ciImport:    *ciImport,
		})
	}
	c.installedGWName = gw.Name
	return nil
}

func (c *MCRouteController) deleteMCNodeFlowsForCIImportEvents(ciImp *mcv1alpha1.ClusterInfoImport, key string) error {
	if err := c.ofClient.UninstallMulticlusterFlows(key); err != nil {
		return fmt.Errorf("failed to uninstall multi-cluster flows to remote Gateway Node %s: %v", ciImp.Name, err)
	}
	c.installedCIImport.Delete(&ciImportInfo{ciImport: *ciImp})
	return nil
}

func (c *MCRouteController) deleteMCNodeFlowsForGatewayEvents(gwName string) error {
	cachedciImps := c.installedCIImport.List()
	for _, ciImp := range cachedciImps {
		cached := ciImp.(*ciImportInfo)
		key := getCacheKeyWithPrefix(gwName + cached.ciImport.Spec.ClusterID)
		if err := c.ofClient.UninstallMulticlusterFlows(key); err != nil {
			return fmt.Errorf("failed to uninstall multi-cluster flows to remote Gateway in ClusterInfoImport %s: %v",
				cached.ciImport.Name, err)
		}
		c.installedCIImport.Delete(ciImp)
	}
	return nil
}

func (c *MCRouteController) getAllCIImports(ciImp *mcv1alpha1.ClusterInfoImport) ([]*mcv1alpha1.ClusterInfoImport, error) {
	var allciImports []*mcv1alpha1.ClusterInfoImport
	var err error
	if ciImp == nil {
		allciImports, err = c.ciImportLister.ClusterInfoImports(c.namespace).List(labels.Everything())
		if err != nil {
			return nil, fmt.Errorf("failed to get ClusterInfoImport list, err: %v", err)
		}
		if len(allciImports) == 0 {
			klog.InfoS("No valid remote ClusterInfo imported, do nothing")
			return nil, nil
		}
	} else {
		allciImports = append(allciImports, ciImp)
	}
	return allciImports, nil
}

// checkActiveGateway will compare Gateway's CreationTimestamp to get active Gateway,
// and also check if given name is a Gateway. The last created Gateway will be the active Gateway.
func (c *MCRouteController) checkActiveGateway() (*mcv1alpha1.Gateway, bool, error) {
	gwlist, err := c.gwLister.Gateways(c.namespace).List(labels.Everything())
	if err != nil {
		klog.ErrorS(err, "Failed to get Gateway list", "namespace", c.namespace)
		return nil, false, err
	}
	if len(gwlist) == 0 {
		return nil, false, fmt.Errorf("no Gateway found in Namespace %s", c.namespace)
	}
	var iAmGW bool
	for _, gw := range gwlist {
		if gw.Name == c.nodeConfig.Name {
			iAmGW = true
			break
		}
	}
	// Comparing Gateway's CreationTimestamp. The last created Gateway will be the first element.
	lastCreatedGW := gwlist[0]
	for _, g := range gwlist {
		gw := g
		if lastCreatedGW.CreationTimestamp.Before(&gw.CreationTimestamp) {
			lastCreatedGW = gw
		}
	}
	return lastCreatedGW, iAmGW, nil
}

func generatePeerConfigs(subnets []string, gatewayIP net.IP) (map[*net.IPNet]net.IP, error) {
	peerConfigs := make(map[*net.IPNet]net.IP, len(subnets))
	for _, subnet := range subnets {
		_, peerCIDR, err := net.ParseCIDR(subnet)
		if err != nil {
			klog.Errorf("Failed to parse subnet %s", subnet)
			return nil, err
		}
		peerConfigs[peerCIDR] = gatewayIP
	}
	return peerConfigs, nil
}

// getPeerGatewayIP will always return the first Gateway IP.
func getPeerGatewayIP(spec mcv1alpha1.ClusterInfo) net.IP {
	if len(spec.GatewayInfos) == 0 {
		return nil
	}
	return net.ParseIP(spec.GatewayInfos[0].GatewayIP)
}

func getCacheKeyWithPrefix(key string) string {
	return "mc-" + key
}
