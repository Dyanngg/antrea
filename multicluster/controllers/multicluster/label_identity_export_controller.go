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
	"crypto/sha1" // #nosec G505: not used for security purposes
	"encoding/hex"
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
	labelIdentityResourceImportNameLength = 16
	// TODO(grayson) evalute what is an appropriate max id
	maxAlloctedID = 65535
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
		allocator:        newIDAllocator(1, maxAlloctedID),
	}
}

// +kubebuilder:rbac:groups=multicluster.crd.antrea.io,resources=resourceexports,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=multicluster.crd.antrea.io,resources=resourceexports/status,verbs=get;update;patch
func (r *LabelIdentityExportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var resExport mcsv1alpha1.ResourceExport
	clusterID, namespace, normalizedLabel := parseLabelIdentityExportNamespacedName(req.NamespacedName)
	if err := r.Client.Get(ctx, req.NamespacedName, &resExport); err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(2).InfoS("ResourceExport is deleted", "resourceexport", req.NamespacedName)
			return ctrl.Result{}, r.onLabelExportDelete(ctx, clusterID, namespace, normalizedLabel)
		}
		return ctrl.Result{}, err
	}
	if err := r.onLabelExportAdd(ctx, clusterID, namespace, normalizedLabel); err != nil {
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

func (r *LabelIdentityExportReconciler) onLabelExportDelete(ctx context.Context, clusterID, namespace, normalizedLabel string) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	originalClusterLabels, ok := r.clusterToLabels[clusterID]
	if !ok || !originalClusterLabels.Has(normalizedLabel) {
		return nil
	}
	return r.refreshLabelIdentityResourceImports(ctx, clusterID, namespace, "", normalizedLabel)
}

func (r *LabelIdentityExportReconciler) onLabelExportAdd(ctx context.Context, clusterID, namespace, normalizedLabel string) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	originalClusterLabels, ok := r.clusterToLabels[clusterID]
	if ok && originalClusterLabels.Has(normalizedLabel) {
		return nil
	}
	return r.refreshLabelIdentityResourceImports(ctx, clusterID, namespace, normalizedLabel, "")
}

// refreshLabelIdentityResourceImports computes the new LabelIdentity kind of
// ResourceImport object to be created, and old LabelIdentity kind of ResourceImport
// object to be deleted.
func (r *LabelIdentityExportReconciler) refreshLabelIdentityResourceImports(ctx context.Context,
	clusterID, namespace, addedClusterLabel, deletedClusterLabel string) error {
	if deletedClusterLabel != "" {
		deletedCluster := sets.NewString(clusterID)
		if clusters, ok := r.labelsToClusters[deletedClusterLabel]; ok && clusters.Equal(deletedCluster) {
			// The cluster where the label identity is being deleted was the only cluster that has
			// the label identity. Hence, the label identity is no longer present in the ClusterSet.
			if !r.handleLabelIdentityDelete(ctx, deletedClusterLabel, clusterID, namespace) {
				return fmt.Errorf("failed to reconcile LabelIdentity kind of ResourceImport for label %v", deletedClusterLabel)
			}
		}
	}
	if addedClusterLabel != "" {
		if _, ok := r.labelsToClusters[addedClusterLabel]; !ok {
			// This is a new label identity in the entire ClusterSet.
			if !r.handleLabelIdentityAdd(ctx, addedClusterLabel, clusterID, namespace) {
				return fmt.Errorf("failed to reconcile LabelIdentity kind of ResourceImport for label %v", deletedClusterLabel)
			}
		}
	}
	return nil
}

// handleLabelIdentityDelete deletes LabelIdentity kind of ResourceImport of label
// identity that no longer exists in the ClusterSet. Note that the ID of a label
// identity is only released if the deletion of its LabelIdentity kind of
// ResourceImport succeeded.
func (r *LabelIdentityExportReconciler) handleLabelIdentityDelete(ctx context.Context,
	deletedLabel, clusterID, namespace string) bool {
	labelIdentityImport := &mcsv1alpha1.ResourceImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hashLabelIdentity(deletedLabel),
			Namespace: namespace,
		},
	}
	if err := r.Client.Delete(ctx, labelIdentityImport, &client.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		klog.ErrorS(err, "Failed to delete LabelIdentity kind of ResourceImport for stale label", "label", deletedLabel)
		return false
	}
	// Delete caches for the label and release the ID assigned for the label
	id := r.labelsToID[deletedLabel]
	r.allocator.release(id)
	delete(r.labelsToID, deletedLabel)
	delete(r.labelsToClusters, deletedLabel)
	if clusterLabels, ok := r.clusterToLabels[clusterID]; ok {
		clusterLabels.Delete(deletedLabel)
	}
	return true
}

// handleLabelIdentityAdd creates LabelIdentity kind of ResourceImport of label
// identity that are added in the ClusterSet. Note that the ID of a label identity
// is only allocated and stored if the creation of its LabelIdentity kind of
// ResourceImport succeeded.
func (r *LabelIdentityExportReconciler) handleLabelIdentityAdd(ctx context.Context,
	addedLabel, clusterID, namespace string) bool {
	id, err := r.allocator.allocate()
	if err != nil {
		klog.ErrorS(err, "Failed to allocate ID for new labels", "label", addedLabel)
		return false
	}
	labelIdentityResImport := getLabelIdentityResImport(addedLabel, namespace, id)
	if err := r.Client.Create(ctx, labelIdentityResImport, &client.CreateOptions{}); err != nil {
		// TODO: check apierrors.IsAlreadyExists(err) to see if it is a hash collision?
		klog.ErrorS(err, "Failed to create LabelIdentity kind of ResourceImport for new label", "label", addedLabel)
		r.allocator.release(id)
		return false
	}
	r.labelsToID[addedLabel] = id
	if clusters, ok := r.labelsToClusters[addedLabel]; ok {
		clusters.Insert(clusterID)
	} else {
		r.labelsToClusters[addedLabel] = sets.NewString(clusterID)
	}
	if labels, ok := r.clusterToLabels[clusterID]; ok {
		labels.Insert(addedLabel)
	} else {
		r.clusterToLabels[clusterID] = sets.NewString(addedLabel)
	}
	return true
}

func getLabelIdentityResImport(label, ns string, id uint32) *mcsv1alpha1.ResourceImport {
	return &mcsv1alpha1.ResourceImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hashLabelIdentity(label),
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

func parseLabelIdentityExportNamespacedName(namespacedName types.NamespacedName) (string, string, string) {
	lastIdx := strings.LastIndex(namespacedName.Name, "-")
	clusterID := namespacedName.Name[:lastIdx]
	normalizedLabel := namespacedName.Name[lastIdx+1:]
	namespace := namespacedName.Namespace
	return clusterID, namespace, normalizedLabel
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
	return hashValue[:labelIdentityResourceImportNameLength]
}
