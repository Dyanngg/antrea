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
	"crypto/sha1"
	"encoding/hex"
	"fmt"
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
	labelIdentityImportNameLength = 16
)

type (
	LabelIdentityExportReconciler struct {
		client.Client
		Scheme           *runtime.Scheme
		mutex            sync.RWMutex
		clusterToLabels  map[string]sets.String
		labelsToClusters map[string]sets.String
		labelsToID       map[string]uint32
		allocator        *idAllocator
	}
)

func NewLabelIdentityExportReconciler(
	client client.Client,
	scheme *runtime.Scheme) *LabelIdentityExportReconciler {
	return &LabelIdentityExportReconciler{
		Client:           client,
		Scheme:           scheme,
		clusterToLabels:  map[string]sets.String{},
		labelsToClusters: map[string]sets.String{},
		labelsToID:       map[string]uint32{},
		allocator:        newIDAllocator(1, 65535),
	}
}

//+kubebuilder:rbac:groups=multicluster.crd.antrea.io,resources=resourceexports,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=multicluster.crd.antrea.io,resources=resourceexports/status,verbs=get;update;patch
func (r *LabelIdentityExportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var resExport mcsv1alpha1.ResourceExport
	if err := r.Client.Get(ctx, req.NamespacedName, &resExport); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	switch resExport.Spec.Kind {
	case common.LabelIdentityKind:
		klog.V(2).InfoS("Reconciling LabelIdentity type of ResourceExport", "resourceexport", req.NamespacedName)
	default:
		return ctrl.Result{}, nil
	}

	if !resExport.DeletionTimestamp.IsZero() {
		// Label identity ResourceExports should not be deleted
		return ctrl.Result{}, nil
	}
	if err := r.onLabelExportUpdate(ctx, &resExport); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LabelIdentityExportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Ignore status update event via GenerationChangedPredicate
	instance := predicate.GenerationChangedPredicate{}
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcsv1alpha1.ResourceExport{}).
		WithEventFilter(instance).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: common.DefaultWorkerCount,
		}).
		Complete(r)
}

func (r *LabelIdentityExportReconciler) onLabelExportUpdate(ctx context.Context, resExport *mcsv1alpha1.ResourceExport) error {
	clusterID := resExport.Spec.ClusterID
	clusterLabelSet := sets.String{}
	for _, l := range resExport.Spec.LabelIdentities.NormalizedLabels {
		clusterLabelSet.Insert(l)
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	originalClusterLabels, ok := r.clusterToLabels[clusterID]
	if ok {
		addedLabels := clusterLabelSet.Difference(originalClusterLabels)
		deletedLabels := originalClusterLabels.Difference(clusterLabelSet)
		return r.refreshLabelIdentityImports(ctx, resExport, addedLabels, deletedLabels, clusterID)
	}
	return r.refreshLabelIdentityImports(ctx, resExport, clusterLabelSet, nil, clusterID)
}

// refreshLabelIdentityImports computes the new LabelIdentityImport objects to be created, and old
// LabelIdentityImport objects to be deleted.
func (r *LabelIdentityExportReconciler) refreshLabelIdentityImports(ctx context.Context,
	resExport *mcsv1alpha1.ResourceExport, addedClusterLabels, deletedClusterLabels sets.String, clusterID string) error {
	var addedLabels []string
	var deletedLabels []string
	for l := range addedClusterLabels {
		if _, ok := r.labelsToClusters[l]; !ok {
			// This is a new label identity in the entire ClusterSet.
			addedLabels = append(addedLabels, l)
		}
	}
	for l := range deletedClusterLabels {
		deletedCluster := sets.NewString(clusterID)
		if clusters, ok := r.labelsToClusters[l]; ok && clusters.Equal(deletedCluster) {
			// The cluster where the label identity is being deleted was the only cluster that has
			// the label identity. Hence, the label identity is no longer present in the ClusterSet.
			deletedLabels = append(deletedLabels, l)
		}
	}
	if len(addedLabels)+len(deletedLabels) == 0 {
		return nil
	}
	reconcileFailedLabels := sets.NewString()
	r.handleLabelIdentitiesDelete(ctx, deletedLabels, reconcileFailedLabels, clusterID, resExport)
	r.handleLabelIdentitiesAdd(ctx, addedLabels, reconcileFailedLabels, clusterID, resExport)
	if len(reconcileFailedLabels) > 0 {
		return fmt.Errorf("failed to reconcile LabelIdentityImport for labels %v", reconcileFailedLabels)
	}
	return nil
}

// handleLabelIdentitiesDelete deletes LabelIdentityImports of label identities that no longer exists
// in the ClusterSet. Note that the ID of a label identity is only released if the deletion of its
// LabelIdentityImport succeeded.
func (r *LabelIdentityExportReconciler) handleLabelIdentitiesDelete(ctx context.Context,
	deletedLabels []string, reconcileFailedLabels sets.String, clusterID string, resExport *mcsv1alpha1.ResourceExport) {
	for _, label := range deletedLabels {
		existingLabelImport := &mcsv1alpha1.LabelIdentityImport{}
		resNamespaced := types.NamespacedName{
			Name:      hashLabelIdentity(label),
			Namespace: resExport.Namespace,
		}
		err := r.Client.Get(ctx, resNamespaced, existingLabelImport)
		if err != nil && !apierrors.IsNotFound(err) {
			klog.ErrorS(err, "failed to get LabelIdentityImport for stale label", "label", label)
			reconcileFailedLabels.Insert(label)
			continue
		}
		if err := r.Client.Delete(ctx, existingLabelImport, &client.DeleteOptions{}); err != nil {
			klog.ErrorS(err, "failed to delete LabelIdentityImport for stale label", "label", label)
			reconcileFailedLabels.Insert(label)
			continue
		}
		// Delete caches for the label and release the ID assigned for the label
		id, _ := r.labelsToID[label]
		r.allocator.release(id)
		delete(r.labelsToID, label)
		delete(r.labelsToClusters, label)
		if clusterLabels, ok := r.clusterToLabels[clusterID]; ok {
			clusterLabels.Delete(label)
		}
	}
}

// handleLabelIdentitiesDelete creates LabelIdentityImports of label identities that are added
// in the ClusterSet. Note that the ID of a label identity is only allocated and stored if the
// creation of its LabelIdentityImport succeeded.
func (r *LabelIdentityExportReconciler) handleLabelIdentitiesAdd(ctx context.Context,
	addedLabels []string, reconcileFailedLabels sets.String, clusterID string, resExport *mcsv1alpha1.ResourceExport) {
	for _, label := range addedLabels {
		id, err := r.allocator.allocate()
		if err != nil {
			klog.ErrorS(err, "failed to allocate ID for new label", "label", label)
			reconcileFailedLabels.Insert(label)
			continue
		}
		labelIdentityImport := getLabelIdentityImport(label, resExport.Namespace, id)
		if err := r.Client.Create(ctx, labelIdentityImport, &client.CreateOptions{}); err != nil {
			// TODO: check apierrors.IsAlreadyExists(err) to see if it is a hash collision?
			klog.ErrorS(err, "failed to create LabelIdentityImport for new label", "label", label)
			r.allocator.release(id)
			reconcileFailedLabels.Insert(label)
			continue
		}
		r.labelsToID[label] = id
		if clusters, ok := r.labelsToClusters[label]; ok {
			clusters.Insert(clusterID)
		} else {
			r.labelsToClusters[label] = sets.NewString(clusterID)
		}
		if labels, ok := r.clusterToLabels[clusterID]; ok {
			labels.Insert(label)
		} else {
			r.clusterToLabels[clusterID] = sets.NewString(label)
		}
	}
}

func getLabelIdentityImport(label, ns string, id uint32) *mcsv1alpha1.LabelIdentityImport {
	return &mcsv1alpha1.LabelIdentityImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hashLabelIdentity(label),
			Namespace: ns,
		},
		Spec: mcsv1alpha1.LabelIdentityImportSpec{
			Label: label,
			ID:    id,
		},
	}
}

func (r *LabelIdentityExportReconciler) deleteResourceExport(resExport *mcsv1alpha1.ResourceExport) (ctrl.Result, error) {
	//resExport.SetFinalizers(common.RemoveStringFromSlice(resExport.Finalizers, common.ResourceExportFinalizer))
	if err := r.Client.Update(ctx, resExport, &client.UpdateOptions{}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// idAllocator allocates an unqiue uint32 ID for each label identity.
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

func (a *idAllocator) release(id uint32) error {
	a.Lock()
	defer a.Unlock()

	a.availableIDs.PushBack(id)
	return nil
}

func newIDAllocator(minID, maxID uint32) *idAllocator {
	availableIDs := list.New()
	return &idAllocator{
		nextID:       minID,
		maxID:        maxID,
		availableIDs: availableIDs,
	}
}

func hashLabelIdentity(l string) string {
	hash := sha1.New() // #nosec G401: not used for security purposes
	hash.Write([]byte(l))
	hashValue := hex.EncodeToString(hash.Sum(nil))
	return hashValue[:labelIdentityImportNameLength]
}
