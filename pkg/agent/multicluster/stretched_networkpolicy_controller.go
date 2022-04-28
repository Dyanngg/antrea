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

package multicluster

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"antrea.io/antrea/multicluster/apis/multicluster/v1alpha1"
	"antrea.io/antrea/multicluster/controllers/multicluster"
	mcinformers "antrea.io/antrea/multicluster/pkg/client/informers/externalversions/multicluster/v1alpha1"
	mclisters "antrea.io/antrea/multicluster/pkg/client/listers/multicluster/v1alpha1"
	"antrea.io/antrea/pkg/agent/interfacestore"
	"antrea.io/antrea/pkg/agent/openflow"
	antreatypes "antrea.io/antrea/pkg/agent/types"
	"antrea.io/antrea/pkg/util/channel"
)

const (
	stretchedNetworkPolicyWorker         = 4
	stretchedNetworkPolicyControllerName = "AntreaAgentStretchedNetworkPolicyController"
)

type podSet map[types.NamespacedName]struct{}

type classifierFlowInfo struct {
	PodRef  types.NamespacedName
	LabelID uint32
}

// StretchedNetworkPolicyController is used to update classifier flows of Pods.
// It will make sure the latest LabelIdentity of the Pod, if available, will be
// loaded into tun_id in the classifier flow of the Pod.
// If the LabelIdentity of the Pod is not available when updating, the
// UnknownLabelIdentity will be loaded. When the actual LabelIdentity is created,
// the classifier flow will be updated accordingly.
type StretchedNetworkPolicyController struct {
	ofClient                  openflow.Client
	interfaceStore            interfacestore.InterfaceStore
	podInformer               cache.SharedIndexInformer
	podLister                 corelisters.PodLister
	podListerSynced           cache.InformerSynced
	namespaceInformer         coreinformers.NamespaceInformer
	namespaceLister           corelisters.NamespaceLister
	namespaceListerSynced     cache.InformerSynced
	labelIdentityInformer     mcinformers.LabelIdentityInformer
	labelIdentityLister       mclisters.LabelIdentityLister
	LabelIdentityListerSynced cache.InformerSynced
	queue                     workqueue.RateLimitingInterface
	lock                      sync.RWMutex

	labelIdentityCache map[string]uint32
	labelToPods        map[string]podSet
	podToLabel         map[types.NamespacedName]string
}

func NewMCAgentStretchedNetworkPolicyController(
	client openflow.Client,
	interfaceStore interfacestore.InterfaceStore,
	podInformer cache.SharedIndexInformer,
	namespaceInformer coreinformers.NamespaceInformer,
	labelIdentityInformer mcinformers.LabelIdentityInformer,
	podUpdateSubscriber channel.Subscriber,
) *StretchedNetworkPolicyController {
	controller := &StretchedNetworkPolicyController{
		ofClient:                  client,
		interfaceStore:            interfaceStore,
		podInformer:               podInformer,
		podLister:                 corelisters.NewPodLister(podInformer.GetIndexer()),
		podListerSynced:           podInformer.HasSynced,
		namespaceInformer:         namespaceInformer,
		namespaceLister:           namespaceInformer.Lister(),
		namespaceListerSynced:     namespaceInformer.Informer().HasSynced,
		labelIdentityInformer:     labelIdentityInformer,
		labelIdentityLister:       labelIdentityInformer.Lister(),
		LabelIdentityListerSynced: labelIdentityInformer.Informer().HasSynced,
		queue:                     workqueue.NewNamedRateLimitingQueue(workqueue.DefaultItemBasedRateLimiter(), "stretchedNetworkPolicy"),
		labelIdentityCache:        map[string]uint32{},
		labelToPods:               map[string]podSet{},
		podToLabel:                map[types.NamespacedName]string{},
	}

	controller.podInformer.AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			// Pod add event will be handled by processPodCNIAddEvent.
			// We choose to use events from podUpdateSubscriber instead of the informer because
			// the controller can only update the Pod classifier flow when the Pod container
			// config is available. Events from the Informer may be received way before the Pod
			// container config is available, which will cause the work item be continually
			// re-queued with an exponential increased delay time. When the Pod container
			// config is ready, the work item could wait for a long time to be processed.
			UpdateFunc: controller.processPodUpdate,
			DeleteFunc: controller.processPodDelete,
		},
		resyncPeriod,
	)
	controller.namespaceInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			UpdateFunc: controller.processNamespaceUpdate,
		},
		resyncPeriod,
	)
	controller.labelIdentityInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(cur interface{}) {
				controller.processLabelIdentityAddOrUpdate(cur)
			},
			UpdateFunc: func(old, cur interface{}) {
				controller.processLabelIdentityAddOrUpdate(cur)
			},
			DeleteFunc: func(old interface{}) {
				controller.processLabelIdentityDelete(old)
			},
		},
		resyncPeriod,
	)
	podUpdateSubscriber.Subscribe(controller.processPodCNIAddEvent)
	return controller
}

func (s *StretchedNetworkPolicyController) Run(stopCh <-chan struct{}) {
	defer s.queue.ShutDown()

	klog.InfoS("Starting controller", "controller", stretchedNetworkPolicyControllerName)
	defer klog.InfoS("Shutting down controller", "controller", stretchedNetworkPolicyControllerName)
	cacheSyncs := []cache.InformerSynced{s.podListerSynced, s.namespaceListerSynced, s.LabelIdentityListerSynced}
	if !cache.WaitForNamedCacheSync(stretchedNetworkPolicyControllerName, stopCh, cacheSyncs...) {
		return
	}
	s.initLabelIDCache()
	s.initPodClassifierFlow()
	for i := 0; i < stretchedNetworkPolicyWorker; i++ {
		go wait.Until(s.worker, time.Second, stopCh)
	}
	<-stopCh
}

func (s *StretchedNetworkPolicyController) initLabelIDCache() {
	labelIdentities, err := s.labelIdentityLister.List(labels.Everything())
	if err == nil {
		for _, labelIdentity := range labelIdentities {
			s.labelIdentityCache[labelIdentity.Spec.Label] = labelIdentity.Spec.ID
		}
	}
}

func (s *StretchedNetworkPolicyController) initPodClassifierFlow() {
	pods, err := s.podLister.List(labels.Everything())
	if err != nil {
		klog.ErrorS(err, "Can't get Pods")
		return
	}
	for _, pod := range pods {
		if pod.Spec.HostNetwork {
			continue
		}
		podNS, err := s.namespaceLister.Get(pod.Namespace)
		if err != nil {
			klog.ErrorS(err, "Can't get the Namespace of a Pod", "name", pod.Name, "namespace", pod.Namespace)
			return
		}
		normalizedLabel := multicluster.GetNormalizedLabel(podNS.Labels, pod.Labels, podNS.Name)
		classifierFlowUpdate := s.getClassifierFlowUpdate(getPodReference(pod), normalizedLabel)
		s.queue.Add(classifierFlowUpdate)
	}
}

// worker is a long-running function that will continually call the processNextWorkItem
// function in order to read and process a message on the workqueue.
func (s *StretchedNetworkPolicyController) worker() {
	for s.processNextWorkItem() {
	}
}

func (s *StretchedNetworkPolicyController) processNextWorkItem() bool {
	obj, quit := s.queue.Get()
	if quit {
		return false
	}
	defer s.queue.Done(obj)

	if c, ok := obj.(*classifierFlowInfo); !ok {
		s.queue.Forget(obj)
		klog.Errorf("Expected type 'classifierFlowInfo' in work queue but got object", "object", obj)
	} else if err := s.syncPodClassifierFlow(c); err == nil {
		s.queue.Forget(c)
	} else {
		// Put the item back on the workqueue to handle any transient errors.
		s.queue.AddRateLimited(c)
		klog.ErrorS(err, "Error syncing classifierFlowInfo, requeuing", "name", c.PodRef.Name, "namespace", c.PodRef.Namespace, "labelID", c.LabelID)
	}
	return true
}

// syncPodClassifierFlow will update labelToPods and podToLabel and update
// PodClassifierFlow with its LabelIdentity, if available, or UnknownLabelIdentity.
func (s *StretchedNetworkPolicyController) syncPodClassifierFlow(classifierFlowUpdate *classifierFlowInfo) error {
	containerConfigs := s.interfaceStore.GetContainerInterfacesByPod(classifierFlowUpdate.PodRef.Name, classifierFlowUpdate.PodRef.Namespace)
	if len(containerConfigs) == 0 {
		return fmt.Errorf("pod container config not found")
	}
	return s.ofClient.UpdatePodClassifierFlowsWithNewLabelID(
		containerConfigs[0].InterfaceName,
		classifierFlowUpdate.LabelID,
		uint32(containerConfigs[0].OFPort),
		containerConfigs[0].IPs,
	)
}

func (s *StretchedNetworkPolicyController) getClassifierFlowUpdate(podRef types.NamespacedName, normalizedLabel string) *classifierFlowInfo {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.getClassifierFlowUpdateLocked(podRef, normalizedLabel)
}

func (s *StretchedNetworkPolicyController) getClassifierFlowUpdateLocked(podRef types.NamespacedName, normalizedLabel string) *classifierFlowInfo {
	oldNormalizedLabel, ok := s.podToLabel[podRef]
	if ok && oldNormalizedLabel != normalizedLabel {
		s.deleteLabelToPod(oldNormalizedLabel, podRef)
	}
	if !ok || oldNormalizedLabel != normalizedLabel {
		s.addLabelToPod(normalizedLabel, podRef)
		s.podToLabel[podRef] = normalizedLabel
	}
	labelID := openflow.UnknownLabelIdentity
	if id, ok := s.labelIdentityCache[normalizedLabel]; ok {
		labelID = id
	}
	return &classifierFlowInfo{
		PodRef:  podRef,
		LabelID: labelID,
	}
}

func (s *StretchedNetworkPolicyController) processPodCNIAddEvent(e interface{}) {
	podEvent := e.(antreatypes.PodUpdate)
	if !podEvent.IsAdd {
		return
	}
	podRef := types.NamespacedName{
		Namespace: podEvent.PodNamespace,
		Name:      podEvent.PodName,
	}
	pod, err := s.podLister.Pods(podRef.Namespace).Get(podRef.Name)
	if err != nil {
		klog.ErrorS(err, "Can't get Pod", "name", podRef.Name, "namespace", podRef.Namespace)
		return
	}
	if pod.Spec.HostNetwork {
		klog.V(2).InfoS("Received a Pod add event but decided to skip",
			"name", pod.Name, "namespace", pod.Namespace, "node", pod.Spec.NodeName, "hostNetwork", pod.Spec.HostNetwork)
		return
	}
	podNS, err := s.namespaceLister.Get(pod.Namespace)
	if err != nil {
		klog.ErrorS(err, "Can't get the Namespace of a Pod", "name", pod.Name, "namespace", pod.Namespace)
		return
	}
	normalizedLabel := multicluster.GetNormalizedLabel(podNS.Labels, pod.Labels, podNS.Name)
	classifierFlowUpdate := s.getClassifierFlowUpdate(podRef, normalizedLabel)
	s.queue.Add(classifierFlowUpdate)
}

// processPodUpdate handles Pod update events. It only enqueues the Pod if the
// Labels of this Pod has been updated.
func (s *StretchedNetworkPolicyController) processPodUpdate(old, cur interface{}) {
	oldPod, oldIsPod := old.(*v1.Pod)
	curPod, curIsPod := cur.(*v1.Pod)
	if !oldIsPod || !curIsPod {
		klog.ErrorS(errors.New("received unexpected object"), "Pod UpdateFunc can't process event", "obj", cur)
		return
	}
	if curPod.Spec.HostNetwork {
		klog.InfoS("received a Pod update event but decided to skip",
			"name", curPod.Name, "namespace", curPod.Namespace, "node", curPod.Spec.NodeName)
		return
	}
	if oldPod != nil && reflect.DeepEqual(oldPod.Labels, curPod.Labels) && oldPod.Spec.NodeName == curPod.Spec.NodeName {
		klog.InfoS("Pod UpdateFunc received a Pod update event, "+
			"but labels and Node names are the same. Skip it", "name", curPod.Name, "namespace", curPod.Namespace)
		return
	}
	podNS, err := s.namespaceLister.Get(curPod.Namespace)
	if err != nil {
		klog.ErrorS(err, "Pod UpdateFunc can't get the Namespace of a Pod", "name", curPod.Name, "namespace", curPod.Namespace)
		return
	}
	normalizedLabel := multicluster.GetNormalizedLabel(podNS.Labels, curPod.Labels, podNS.Name)
	classifierFlowUpdate := s.getClassifierFlowUpdate(getPodReference(curPod), normalizedLabel)
	s.queue.Add(classifierFlowUpdate)
}

// processPodDelete handles Pod delete events. It deletes the Pod from the
// labelToPods and podToLabel. After Pod is deleted, its classifier flow will also
// be deleted by podConfigurator. So no need to enqueue this Pod to update its
// classifier flow.
func (s *StretchedNetworkPolicyController) processPodDelete(old interface{}) {
	oldPod, oldIsPod := old.(*v1.Pod)
	if !oldIsPod {
		klog.ErrorS(errors.New("received unexpected object"), "Pod DeleteFunc can't process event", "obj", old)
		return
	}
	oldPodRef := getPodReference(oldPod)
	s.lock.Lock()
	defer s.lock.Unlock()
	podLabel := s.podToLabel[oldPodRef]
	s.deleteLabelToPod(podLabel, oldPodRef)
	delete(s.podToLabel, oldPodRef)
}

// processNamespaceUpdate handles Namespace update events. It only enqueues all
// Pods in this Namespace if the Labels of this Namespace has been updated.
func (s *StretchedNetworkPolicyController) processNamespaceUpdate(old, cur interface{}) {
	oldNS, oldIsNS := old.(*v1.Namespace)
	curNS, curIsNS := cur.(*v1.Namespace)
	if !oldIsNS || !curIsNS {
		klog.ErrorS(errors.New("received unexpected object"), "Namespace UpdateFunc can't process event", "obj", cur)
		return
	}
	if reflect.DeepEqual(oldNS.Labels, curNS.Labels) {
		klog.V(2).InfoS("Namespace UpdateFunc received a Namespace update event, but labels are the same. Skip it", "namespace", curNS.Name)
		return
	}
	allPodsInNS, err := s.podLister.Pods(curNS.Name).List(labels.Everything())
	if err != nil {
		klog.ErrorS(err, "Namespace UpdateFunc can't list Pods in the Namespace", "namespace", curNS.Name)
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, pod := range allPodsInNS {
		if pod.Spec.HostNetwork {
			continue
		}
		normalizedLabel := multicluster.GetNormalizedLabel(curNS.Labels, pod.Labels, curNS.Name)
		classifierFlowUpdate := s.getClassifierFlowUpdateLocked(getPodReference(pod), normalizedLabel)
		s.queue.Add(classifierFlowUpdate)
	}
}

// processLabelIdentityAddOrUpdate handles labelIdentity add or update event. It will
// 1. Add/Update labelIdentity to labelIdentityCache
// 2. Enqueue all Pods with this label Identity
func (s *StretchedNetworkPolicyController) processLabelIdentityAddOrUpdate(cur interface{}) {
	labelIdentity, isLabelID := cur.(*v1alpha1.LabelIdentity)
	if !isLabelID {
		klog.ErrorS(errors.New("received unexpected object"), "LabelIdentity AddOrUpdateFunc can't process event", "obj", cur)
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	s.labelIdentityCache[labelIdentity.Spec.Label] = labelIdentity.Spec.ID
	if podSet, ok := s.labelToPods[labelIdentity.Spec.Label]; ok {
		for podRef := range podSet {
			classifierFlowUpdate := s.getClassifierFlowUpdateLocked(podRef, s.podToLabel[podRef])
			s.queue.Add(classifierFlowUpdate)
		}
	}
}

func (s *StretchedNetworkPolicyController) processLabelIdentityDelete(old interface{}) {
	labelIdentity, isLabelID := old.(*v1alpha1.LabelIdentity)
	if !isLabelID {
		klog.ErrorS(errors.New("received unexpected object"), "LabelIdentity DeleteFunc can't process event", "obj", old)
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	delete(s.labelIdentityCache, labelIdentity.Spec.Label)
	if podSet, ok := s.labelToPods[labelIdentity.Spec.Label]; ok {
		for podRef := range podSet {
			classifierFlowUpdate := s.getClassifierFlowUpdateLocked(podRef, s.podToLabel[podRef])
			s.queue.Add(classifierFlowUpdate)
		}
	}
}

func (s *StretchedNetworkPolicyController) addLabelToPod(normalizedLabel string, podRef types.NamespacedName) {
	if _, ok := s.labelToPods[normalizedLabel]; ok {
		s.labelToPods[normalizedLabel][podRef] = struct{}{}
	} else {
		s.labelToPods[normalizedLabel] = podSet{podRef: struct{}{}}
	}
}

func (s *StretchedNetworkPolicyController) deleteLabelToPod(normalizedLabel string, podRef types.NamespacedName) {
	if _, ok := s.labelToPods[normalizedLabel]; ok {
		delete(s.labelToPods[normalizedLabel], podRef)
		if len(s.labelToPods[normalizedLabel]) == 0 {
			delete(s.labelToPods, normalizedLabel)
		}
	}
}

func getPodReference(pod *v1.Pod) types.NamespacedName {
	return types.NamespacedName{
		Name:      pod.Name,
		Namespace: pod.Namespace,
	}
}
