/*
Copyright 2022 Antrea Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package multicluster

import (
	"context"
	"sync"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mcsv1alpha1 "antrea.io/antrea/multicluster/apis/multicluster/v1alpha1"
	"antrea.io/antrea/multicluster/controllers/multicluster/common"
	"antrea.io/antrea/multicluster/controllers/multicluster/commonarea"
)

// LabelIdentityReconciler watches relevant Pod and Namespace events in the member cluster,
// computes the list of unique label identities in the cluster, and exports it to the
// leader cluster for further processing.
type (
	LabelIdentityReconciler struct {
		client.Client
		Scheme           *runtime.Scheme
		commonAreaGetter RemoteCommonAreaGetter
		remoteCommonArea commonarea.RemoteCommonArea
		// labelMutex prevents concurrent access to labelToPodsCache and podLabelCache
		labelMutex sync.RWMutex
		// resourceExportMutex prevents concrrent updates to the cluster label identity ResourceExport
		resourceExportMutex sync.Mutex
		// labelToPodsCache stores mapping from label identities to Pods that have this label identity
		labelToPodsCache map[string]sets.String
		// podLabelCache stores mapping from Pods to their label identities
		podLabelCache  map[string]string
		localClusterID string
	}
)

func NewLabelIdentityReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	commonAreaGetter RemoteCommonAreaGetter) *LabelIdentityReconciler {
	return &LabelIdentityReconciler{
		Client:           client,
		Scheme:           scheme,
		commonAreaGetter: commonAreaGetter,
		labelToPodsCache: map[string]sets.String{},
		podLabelCache:    map[string]string{},
	}
}

//+kubebuilder:rbac:groups=multicluster.crd.antrea.io,resources=resourceexports,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
func (r *LabelIdentityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.V(2).InfoS("Reconciling Pod/Namespace for label identity", "pod", req.NamespacedName)
	var commonArea commonarea.RemoteCommonArea
	var err error
	commonArea, r.localClusterID, err = r.commonAreaGetter.GetRemoteCommonAreaAndLocalID()
	if commonArea == nil {
		return ctrl.Result{Requeue: true}, err
	}
	r.remoteCommonArea = commonArea
	var pod v1.Pod
	var ns v1.Namespace

	if err := r.Client.Get(ctx, req.NamespacedName, &ns); err == nil && ns.DeletionTimestamp.IsZero() {
		// Based on the predicates used to register the reconciler, this can only be a
		// Namespace add or Namespace label update event
		return ctrl.Result{}, r.onNamespaceUpdate(ctx, req.Name, ns.Labels)
	}
	if err := r.Client.Get(ctx, req.NamespacedName, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(2).InfoS("Pod is deleted", "pod", req.NamespacedName)
			return ctrl.Result{}, r.onPodDelete(ctx, req.NamespacedName.String())
		}
		return ctrl.Result{}, err
	}
	if pod.Spec.HostNetwork {
		klog.V(2).InfoS("Skip reconciliation for host-network Pod", "pod", req.NamespacedName)
		return ctrl.Result{}, nil
	}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: req.Namespace}, &ns); err != nil {
		klog.ErrorS(err, "Cannot get Namespace of Pod to be reconciled", "pod", req.NamespacedName)
		return ctrl.Result{}, err
	}
	nsLabels, podLabels := ns.Labels, pod.Labels
	return ctrl.Result{}, r.onPodUpdate(ctx, req.NamespacedName.String(), getNormalizedLabel(nsLabels, podLabels))
}

// SetupWithManager sets up the controller with the Manager.
func (r *LabelIdentityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Ignore status update event via GenerationChangedPredicate
	instance := predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Pod{}).
		WithEventFilter(instance).
		Watches(&source.Kind{Type: &v1.Namespace{}},
			handler.EnqueueRequestsFromMapFunc(namespaceMapFunc)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: common.DefaultWorkerCount,
		}).
		Complete(r)
}

// namespaceMapFunc enqueues Namespace events to the reconciler.
func namespaceMapFunc(o client.Object) []reconcile.Request {
	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      o.GetName(),
				Namespace: o.GetNamespace(),
			},
		},
	}
}

// onPodDelete removes the Pod and label identity mapping from the cache, and updates the cluster's
// label identity ResourceExport if necessary (the Pod deletion event causes some label identities
// to no longer being present in the cluster).
func (r *LabelIdentityReconciler) onPodDelete(ctx context.Context, podKey string) error {
	if resExportNeedUpdate := r.deleteLabelMapping(podKey, ""); resExportNeedUpdate {
		labelsInCluster := r.getLabelsInCluster()
		if err := r.refreshLabelIdentityResourceExport(ctx, labelsInCluster); err != nil {
			return err
		}
	}
	return nil
}

func (r *LabelIdentityReconciler) deleteLabelMapping(podKey, normalizedLabel string) (resExportNeedUpdate bool) {
	r.labelMutex.Lock()
	defer r.labelMutex.Unlock()

	resExportNeedUpdate = false
	originalLabel, ok := r.podLabelCache[podKey]
	if !ok {
		// Might be a host-network Pod deletion
		return resExportNeedUpdate
	}
	if normalizedLabel == "" {
		// Use the cached label identity for the Pod to clean up labelToPodsCache
		normalizedLabel = originalLabel
	}
	delete(r.podLabelCache, podKey)
	if podNames, ok := r.labelToPodsCache[normalizedLabel]; ok {
		podNames.Delete(podKey)
		if len(podNames) == 0 {
			klog.V(2).InfoS("Label no longer exists in the cluster", "label", normalizedLabel)
			delete(r.labelToPodsCache, normalizedLabel)
			resExportNeedUpdate = true
		}
	}
	return resExportNeedUpdate
}

func (r *LabelIdentityReconciler) checkPodLabelCache(pod, normalizedLabel string) (string, bool) {
	r.labelMutex.RLock()
	defer r.labelMutex.RUnlock()

	cachedPodLabel, labelChanged := "", false
	cachedPodLabel, ok := r.podLabelCache[pod]
	if !ok {
		labelChanged = true
	} else if cachedPodLabel != normalizedLabel {
		labelChanged = true
	}
	return cachedPodLabel, labelChanged
}

// onPodUpdate updates the Pod and label identity mapping in the cache, and updates the cluster's
// label identity ResourceExport if necessary (the Pod creation event causes some new label identities
// to appear in the cluster).
func (r *LabelIdentityReconciler) onPodUpdate(ctx context.Context, podKey, normalizedLabel string) error {
	if resExportNeedUpdate := r.updateLabelMapping(podKey, normalizedLabel); resExportNeedUpdate {
		labelsInCluster := r.getLabelsInCluster()
		if err := r.refreshLabelIdentityResourceExport(ctx, labelsInCluster); err != nil {
			return err
		}
	}
	return nil
}

// onPodUpdates updates Pod and label identity relations in batch, and updates the cluster's label identity
// ResourceExport if ncessary.
func (r *LabelIdentityReconciler) onPodUpdates(ctx context.Context, labelUpdates map[string]string) error {
	resExportNeedUpdate := false
	for podKey, normalizedLabel := range labelUpdates {
		if updated := r.updateLabelMapping(podKey, normalizedLabel); updated {
			resExportNeedUpdate = true
		}
	}
	if resExportNeedUpdate {
		labelsInCluster := r.getLabelsInCluster()
		if err := r.refreshLabelIdentityResourceExport(ctx, labelsInCluster); err != nil {
			return err
		}
	}
	return nil
}

func (r *LabelIdentityReconciler) updateLabelMapping(podKey, normalizedLabel string) bool {
	cachedLabel, podLabelsUpdated := r.checkPodLabelCache(podKey, normalizedLabel)
	if !podLabelsUpdated {
		return false
	}
	if cachedLabel != "" {
		// Remove any existing Pod-label mappings
		podLabelsUpdated = r.deleteLabelMapping(podKey, cachedLabel)
	}
	r.labelMutex.Lock()
	defer r.labelMutex.Unlock()
	r.podLabelCache[podKey] = normalizedLabel
	if _, ok := r.labelToPodsCache[normalizedLabel]; !ok {
		r.labelToPodsCache[normalizedLabel] = sets.NewString(podKey)
		return true
	}
	r.labelToPodsCache[normalizedLabel].Insert(podKey)
	// Update to the LabelIdentity-type ResourceExport might still be needed
	// if the original LabelIdentity of the Pod is no longer present in the cluster
	return podLabelsUpdated
}

// onNamespaceUpdate updates the label identities cached for all Pods in the Namespace
// in case the Namespace's labels change
func (r *LabelIdentityReconciler) onNamespaceUpdate(ctx context.Context, namespace string, nsLabels map[string]string) error {
	var pods v1.PodList
	if err := r.Client.List(ctx, &pods, &client.ListOptions{Namespace: namespace}); err != nil {
		klog.Errorf("Failed to list current Pods in Namespace %s", namespace)
		return err
	}
	labelUpdates := map[string]string{}
	for _, p := range pods.Items {
		if p.Spec.HostNetwork {
			// Skip reconciliation for host-network Pods
			continue
		}
		podKey, updatedLabel := p.Namespace+"/"+p.Name, getNormalizedLabel(nsLabels, p.Labels)
		labelUpdates[podKey] = updatedLabel
		klog.V(4).InfoS("Re-processing Pod for label identity sync due to Namespace label updates", "pod", podKey)
	}
	if err := r.onPodUpdates(ctx, labelUpdates); err != nil {
		return err
	}
	return nil
}

func (r *LabelIdentityReconciler) refreshLabelIdentityResourceExport(ctx context.Context, labelsInCluster []string) error {
	r.resourceExportMutex.Lock()
	defer r.resourceExportMutex.Unlock()

	existingResExport := &mcsv1alpha1.ResourceExport{}
	resNamespaced := types.NamespacedName{
		Name:      getResourceExportNameForLabelIdentity(r.localClusterID),
		Namespace: r.remoteCommonArea.GetNamespace(),
	}
	err := r.remoteCommonArea.Get(ctx, resNamespaced, existingResExport)
	if err != nil && !apierrors.IsNotFound(err) {
		klog.ErrorS(err, "Failed to get ResourceExport in remote cluster", "resourceexport", resNamespaced)
		return err
	}
	if apierrors.IsNotFound(err) {
		labelResExport := r.getLabelIdentityResourceExport(r.remoteCommonArea.GetNamespace(), resNamespaced.Name, labelsInCluster)
		if err = r.remoteCommonArea.Create(ctx, labelResExport, &client.CreateOptions{}); err != nil {
			return err
		}
		return nil
	}
	// Update the cluster's label identities
	existingResExport.Spec.LabelIdentities = &mcsv1alpha1.LabelIdentityExport{
		NormalizedLabels: labelsInCluster,
	}
	if err = r.remoteCommonArea.Update(ctx, existingResExport, &client.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}

func (r *LabelIdentityReconciler) getLabelIdentityResourceExport(resExportNamespace, name string, normalizedLabels []string) *mcsv1alpha1.ResourceExport {
	return &mcsv1alpha1.ResourceExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: resExportNamespace,
			Labels: map[string]string{
				common.SourceKind:      common.LabelIdentityKind,
				common.SourceClusterID: r.localClusterID,
			},
		},
		Spec: mcsv1alpha1.ResourceExportSpec{
			ClusterID: r.localClusterID,
			Kind:      common.LabelIdentityKind,
			LabelIdentities: &mcsv1alpha1.LabelIdentityExport{
				NormalizedLabels: normalizedLabels,
			},
		},
	}
}

func (r *LabelIdentityReconciler) getLabelsInCluster() []string {
	r.labelMutex.RLock()
	defer r.labelMutex.RUnlock()

	labelsInCluster, idx := make([]string, len(r.labelToPodsCache)), 0
	for label := range r.labelToPodsCache {
		labelsInCluster[idx] = label
		idx++
	}
	return labelsInCluster
}

func getNormalizedLabel(nsLabels, podLabels map[string]string) string {
	return "namespace:" + labels.FormatLabels(nsLabels) + "&pod:" + labels.FormatLabels(podLabels)
}

// getResourceExportNameForLabelIdentity retrieves the ResourceExport name for exporting
// label identities in that cluster.
func getResourceExportNameForLabelIdentity(clusterID string) string {
	return clusterID + "-labels"
}
