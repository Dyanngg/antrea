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

package commonarea

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	multiclusterv1alpha1 "antrea.io/antrea/multicluster/apis/multicluster/v1alpha1"
	"antrea.io/antrea/multicluster/controllers/multicluster/common"
)

func labelIdentityImportIndexerKeyFunc(obj interface{}) (string, error) {
	ri := obj.(multiclusterv1alpha1.LabelIdentityImport)
	return common.NamespacedName(ri.Namespace, ri.Name), nil
}

// LabelIdentityImportReconciler reconciles a LabelIdentityImport object in the member cluster.
type LabelIdentityImportReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	localClusterClient    client.Client
	localClusterID        string
	namespace             string
	remoteCommonArea      RemoteCommonArea
	installedLabelImports cache.Indexer
}

func NewLabelIdentityImportReconciler(client client.Client, scheme *runtime.Scheme, localClusterClient client.Client,
	localClusterID string, namespace string, remoteCommonArea RemoteCommonArea) *LabelIdentityImportReconciler {
	return &LabelIdentityImportReconciler{
		Client:                client,
		Scheme:                scheme,
		localClusterClient:    localClusterClient,
		localClusterID:        localClusterID,
		namespace:             namespace,
		remoteCommonArea:      remoteCommonArea,
		installedLabelImports: cache.NewIndexer(labelIdentityImportIndexerKeyFunc, cache.Indexers{}),
	}
}

//+kubebuilder:rbac:groups=multicluster.crd.antrea.io,resources=labelidentityimports,verbs=get;list;watch;create;update;patch;delete
func (r *LabelIdentityImportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.V(2).InfoS("Reconciling LabelIdentityImport", "labelidentityimport", req.NamespacedName)
	if r.localClusterClient == nil {
		return ctrl.Result{}, errors.New("localClusterClient has not been initialized properly, no local cluster client")
	}
	if r.remoteCommonArea == nil {
		return ctrl.Result{}, errors.New("remoteCommonArea has not been initialized properly, no remote common area")
	}
	var labelIdentityImport multiclusterv1alpha1.LabelIdentityImport
	err := r.remoteCommonArea.Get(ctx, req.NamespacedName, &labelIdentityImport)
	isDeleted := apierrors.IsNotFound(err)
	if err != nil {
		if !isDeleted {
			klog.InfoS("Unable to fetch LabelIdentityImport", "labelidentityimport", req.NamespacedName.String(), "err", err)
			return ctrl.Result{}, err
		}
		labelIdentityImportObj, exist, _ := r.installedLabelImports.GetByKey(req.NamespacedName.String())
		if !exist {
			// Stale controller will clean up any stale Label Identities
			klog.ErrorS(err, "No cached data for LabelIdentityImport", "labelidentityimport", req.NamespacedName)
			return ctrl.Result{}, nil
		}
		labelIdentityImport = labelIdentityImportObj.(multiclusterv1alpha1.LabelIdentityImport)
		return r.handleLabelIdentityImpDelete(ctx, &labelIdentityImport)
	}
	return r.handleLabelIdentityImpUpdate(ctx, &labelIdentityImport)
}

func (r *LabelIdentityImportReconciler) handleLabelIdentityImpUpdate(ctx context.Context,
	labelImp *multiclusterv1alpha1.LabelIdentityImport) (ctrl.Result, error) {
	labelIdentityName := types.NamespacedName{
		Namespace: "",
		Name:      labelImp.Name,
	}
	labelIdentity := &multiclusterv1alpha1.LabelIdentity{}
	err := r.localClusterClient.Get(ctx, labelIdentityName, labelIdentity)
	labelIdentityNotFound := apierrors.IsNotFound(err)
	if err != nil && !labelIdentityNotFound {
		return ctrl.Result{}, err
	}
	if labelIdentityNotFound {
		newLabelIdentity := &multiclusterv1alpha1.LabelIdentity{
			ObjectMeta: metav1.ObjectMeta{
				Name: labelImp.Name,
			},
			Spec: multiclusterv1alpha1.LabelIdentityImportSpec{
				Label: labelImp.Spec.Label,
				ID:    labelImp.Spec.ID,
			},
		}
		if err = r.localClusterClient.Create(ctx, newLabelIdentity, &client.CreateOptions{}); err != nil {
			klog.ErrorS(err, "Failed to create LabelIdentity", "clusterID", r.localClusterID, "label", labelImp.Spec.Label)
			return ctrl.Result{}, err
		}
		r.installedLabelImports.Add(*labelImp)
	} else {
		// TODO: is this branch necessary?
		if labelIdentity.Spec.ID != labelImp.Spec.ID {
			labelIdentity.Spec.ID = labelImp.Spec.ID
			if err = r.localClusterClient.Update(ctx, labelIdentity, &client.UpdateOptions{}); err != nil {
				klog.ErrorS(err, "Failed to update LabelIdentity", "clusterID", r.localClusterID, "label", labelImp.Spec.Label)
				return ctrl.Result{}, err
			}
		}
	}
	return ctrl.Result{}, nil
}

func (r *LabelIdentityImportReconciler) handleLabelIdentityImpDelete(ctx context.Context,
	labelImp *multiclusterv1alpha1.LabelIdentityImport) (ctrl.Result, error) {
	labelIdentityName := types.NamespacedName{
		Namespace: "",
		Name:      labelImp.Name,
	}
	klog.V(2).InfoS("Deleting LabelIdentity corresponding to LabelIdentityImport", "labelidentityimport", klog.KObj(labelImp))
	labelIdentity := &multiclusterv1alpha1.LabelIdentity{}
	err := r.localClusterClient.Get(ctx, labelIdentityName, labelIdentity)
	if err != nil {
		klog.V(2).InfoS("Unable to fetch LabelIdentity", "labelidentity", labelIdentityName.Name, "err", err)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	err = r.localClusterClient.Delete(ctx, labelIdentity, &client.DeleteOptions{})
	if err != nil {
		klog.ErrorS(err, "Failed to delete LabelIdentity", "labelidentity", labelIdentityName.Name)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

func (r *LabelIdentityImportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Ignore status update event via GenerationChangedPredicate
	instance := predicate.GenerationChangedPredicate{}
	return ctrl.NewControllerManagedBy(mgr).
		For(&multiclusterv1alpha1.LabelIdentityImport{}).
		WithEventFilter(instance).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: common.DefaultWorkerCount,
		}).
		Complete(r)
}
