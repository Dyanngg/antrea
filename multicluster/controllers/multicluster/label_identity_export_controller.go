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
	"container/list"
	"context"
	"fmt"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	mcsv1alpha1 "antrea.io/antrea/multicluster/apis/multicluster/v1alpha1"
	"antrea.io/antrea/multicluster/controllers/multicluster/common"
)

const (
	// TODO(grayson) evalute what is an appropriate max id
	maxAlloctedID = 65535
)

type (
	LabelIdentityExportReconciler struct {
		client.Client
		Scheme           *runtime.Scheme
		mutex            sync.RWMutex
		namespace        string
		clusterToLabels  map[string]sets.String
		labelsToClusters map[string]sets.String
		labelsToID       map[string]uint32
		allocator        *idAllocator
	}
)

func NewLabelIdentityExportReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	namespace string) *LabelIdentityExportReconciler {
	return &LabelIdentityExportReconciler{
		Client:           client,
		Scheme:           scheme,
		namespace:        namespace,
		clusterToLabels:  map[string]sets.String{},
		labelsToClusters: map[string]sets.String{},
		labelsToID:       map[string]uint32{},
		allocator:        newIDAllocator(1, maxAlloctedID),
	}
}

// +kubebuilder:rbac:groups=multicluster.crd.antrea.io,resources=resourceexports,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=multicluster.crd.antrea.io,resources=resourceexports/status,verbs=get;update;patch
func (r *LabelIdentityExportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var resExport mcsv1alpha1.ResourceExport
	clusterID, labelHash := parseLabelIdentityExportNamespacedName(req.NamespacedName)
	if err := r.Client.Get(ctx, req.NamespacedName, &resExport); err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(2).InfoS("ResourceExport is deleted", "resourceexport", req.NamespacedName)
			return ctrl.Result{}, r.onLabelExportDelete(ctx, clusterID, labelHash)
		}
		return ctrl.Result{}, err
	}
	normalizedLabel := resExport.Spec.LabelIdentity.NormalizedLabel
	if err := r.onLabelExportAdd(ctx, clusterID, labelHash, normalizedLabel); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LabelIdentityExportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Ignore status update event via GenerationChangedPredicate
	generationPredicate := predicate.GenerationChangedPredicate{}
	// Only register this controller to reconcile LabelIdentity kind of ResourceExport
	labelIdentityResExportFilter := func(object client.Object) bool {
		if resExport, ok := object.(*mcsv1alpha1.ResourceExport); ok {
			return resExport.Spec.Kind == common.LabelIdentityKind
		}
		return false
	}
	labelIdentityResExportPredicate := predicate.NewPredicateFuncs(labelIdentityResExportFilter)
	instance := predicate.And(generationPredicate, labelIdentityResExportPredicate)
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcsv1alpha1.ResourceExport{}).
		WithEventFilter(instance).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: common.DefaultWorkerCount,
		}).
		Complete(r)
}

func (r *LabelIdentityExportReconciler) onLabelExportDelete(ctx context.Context, clusterID, labelHash string) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	originalClusterLabels, ok := r.clusterToLabels[clusterID]
	if !ok || !originalClusterLabels.Has(labelHash) {
		return nil
	}
	return r.cleanupLabelIdentityResourceImports(ctx, clusterID, labelHash)
}

func (r *LabelIdentityExportReconciler) onLabelExportAdd(ctx context.Context, clusterID, labelHash, label string) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	originalClusterLabels, ok := r.clusterToLabels[clusterID]
	if ok && originalClusterLabels.Has(labelHash) {
		return nil
	}
	return r.refreshLabelIdentityResourceImports(ctx, clusterID, labelHash, label)
}

// refreshLabelIdentityResourceImports creates new LabelIdentity kind of
// ResourceImport object if needed.
func (r *LabelIdentityExportReconciler) refreshLabelIdentityResourceImports(ctx context.Context,
	clusterID, addedClusterLabelHash, addedLabel string) error {
	if _, ok := r.labelsToClusters[addedClusterLabelHash]; !ok {
		// This is a new label identity in the entire ClusterSet.
		if !r.handleLabelIdentityAdd(ctx, addedClusterLabelHash, addedLabel, clusterID, r.namespace) {
			return fmt.Errorf("failed to reconcile LabelIdentity kind of ResourceImport for label %v", addedClusterLabelHash)
		}
	}
	return nil
}

// cleanupLabelIdentityResourceImports deletes stale LabelIdentity kind of
// ResourceImport object if needed.
func (r *LabelIdentityExportReconciler) cleanupLabelIdentityResourceImports(ctx context.Context,
	clusterID, deletedClusterLabelHash string) error {
	deletedCluster := sets.NewString(clusterID)
	if clusters, ok := r.labelsToClusters[deletedClusterLabelHash]; ok && clusters.Equal(deletedCluster) {
		// The cluster where the label identity is being deleted was the only cluster that has
		// the label identity. Hence, the label identity is no longer present in the ClusterSet.
		if !r.handleLabelIdentityDelete(ctx, deletedClusterLabelHash, clusterID, r.namespace) {
			return fmt.Errorf("failed to reconcile LabelIdentity kind of ResourceImport for label %v", deletedClusterLabelHash)
		}
	}
	return nil
}

// handleLabelIdentityDelete deletes the ResourceImport of a label identity
// hash that no longer exists in the ClusterSet. Note that the ID of a label
// identity hash is only released if the deletion of its corresponding
// ResourceImport succeeded.
func (r *LabelIdentityExportReconciler) handleLabelIdentityDelete(ctx context.Context,
	deletedLabelHash, clusterID, namespace string) bool {
	labelIdentityImport := &mcsv1alpha1.ResourceImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deletedLabelHash,
			Namespace: namespace,
		},
	}
	if err := r.Client.Delete(ctx, labelIdentityImport, &client.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		klog.ErrorS(err, "Failed to delete LabelIdentity kind of ResourceImport for stale label", "label", deletedLabelHash)
		return false
	}
	// Delete caches for the label and release the ID assigned for the label
	id := r.labelsToID[deletedLabelHash]
	r.allocator.release(id)
	delete(r.labelsToID, deletedLabelHash)
	delete(r.labelsToClusters, deletedLabelHash)
	if clusterLabels, ok := r.clusterToLabels[clusterID]; ok {
		clusterLabels.Delete(deletedLabelHash)
	}
	return true
}

// handleLabelIdentityAdd creates ResourceImport of a label identity that is added in
// the ClusterSet. Note that the ID of a label identity is only allocated and stored
// if the creation of its corresponding ResourceImport succeeded.
func (r *LabelIdentityExportReconciler) handleLabelIdentityAdd(ctx context.Context,
	labelHash, label, clusterID, namespace string) bool {
	id, err := r.allocator.allocate()
	if err != nil {
		klog.ErrorS(err, "Failed to allocate ID for new labels", "label", labelHash)
		return false
	}
	labelIdentityResImport := getLabelIdentityResImport(labelHash, label, namespace, id)
	if err := r.Client.Create(ctx, labelIdentityResImport, &client.CreateOptions{}); err != nil {
		klog.ErrorS(err, "Failed to create LabelIdentity kind of ResourceImport for new label", "label", label)
		r.allocator.release(id)
		return false
	}
	r.labelsToID[labelHash] = id
	if clusters, ok := r.labelsToClusters[labelHash]; ok {
		clusters.Insert(clusterID)
	} else {
		r.labelsToClusters[labelHash] = sets.NewString(clusterID)
	}
	if labels, ok := r.clusterToLabels[clusterID]; ok {
		labels.Insert(labelHash)
	} else {
		r.clusterToLabels[clusterID] = sets.NewString(labelHash)
	}
	return true
}

func getLabelIdentityResImport(labelHash, label, ns string, id uint32) *mcsv1alpha1.ResourceImport {
	return &mcsv1alpha1.ResourceImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      labelHash,
			Namespace: ns,
		},
		Spec: mcsv1alpha1.ResourceImportSpec{
			LabelIdentity: &mcsv1alpha1.LabelIdentitySpec{
				Label: label,
				ID:    id,
			},
		},
	}
}

// parseLabelIdentityExportNamespacedName gets the clusterID and label identity
// hash from the API request.
func parseLabelIdentityExportNamespacedName(namespacedName types.NamespacedName) (string, string) {
	lastIdx := strings.LastIndex(namespacedName.Name, "-")
	clusterID := namespacedName.Name[:lastIdx]
	labelHash := namespacedName.Name[lastIdx+1:]
	return clusterID, labelHash
}

// idAllocator allocates a unqiue uint32 ID for each label identity.
type idAllocator struct {
	sync.Mutex
	maxID        uint32
	nextID       uint32
	availableIDs *list.List
}

func (a *idAllocator) allocate() (uint32, error) {
	a.Lock()
	defer a.Unlock()

	front := a.availableIDs.Front()
	if front != nil {
		return a.availableIDs.Remove(front).(uint32), nil
	}
	if a.nextID <= a.maxID {
		allocated := a.nextID
		a.nextID += 1
		return allocated, nil
	}
	return 0, fmt.Errorf("no ID available")
}

func (a *idAllocator) release(id uint32) {
	a.Lock()
	defer a.Unlock()

	a.availableIDs.PushBack(id)
}

func newIDAllocator(minID, maxID uint32) *idAllocator {
	availableIDs := list.New()
	return &idAllocator{
		nextID:       minID,
		maxID:        maxID,
		availableIDs: availableIDs,
	}
}
