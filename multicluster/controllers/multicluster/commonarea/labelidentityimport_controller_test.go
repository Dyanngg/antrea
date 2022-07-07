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
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcsv1alpha1 "antrea.io/antrea/multicluster/apis/multicluster/v1alpha1"
	"antrea.io/antrea/multicluster/controllers/multicluster/common"
)

var (
	labelIdentityResImportName = "label-identity-app-client"

	labelIdentityResImpReq = ctrl.Request{NamespacedName: types.NamespacedName{
		Name: labelIdentityResImportName,
	}}

	labelIdentityResImport = &mcsv1alpha1.ResourceImport{
		ObjectMeta: metav1.ObjectMeta{
			Name: labelIdentityResImportName,
		},
		Spec: mcsv1alpha1.ResourceImportSpec{
			LabelIdentity: &mcsv1alpha1.LabelIdentitySpec{
				Label: "namespace:kubernetes.io/metadata.name=ns&pod:app=client",
				ID:    uint32(1),
			},
		},
	}
)

func TestLabelIdentityResourceImportReconciler_handleLabelIdentityCreateEvent(t *testing.T) {
	remoteMgr := NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(labelIdentityResImport).Build()
	remoteCluster := NewFakeRemoteCommonArea(scheme, remoteMgr, fakeRemoteClient, "leader-cluster", "default")

	r := NewLabelIdentityResourceImportReconciler(fakeClient, scheme, fakeClient, localClusterID, "default", remoteCluster)

	if _, err := r.Reconcile(ctx, labelIdentityResImpReq); err != nil {
		t.Errorf("LabelIdentityResourceImport Reconciler should handle LabelIdentity create event successfully but got error = %v", err)
	} else {
		labelIdentity := &mcsv1alpha1.LabelIdentity{}
		if err := fakeClient.Get(ctx, types.NamespacedName{Namespace: "", Name: labelIdentityResImportName}, labelIdentity); err != nil {
			t.Errorf("LabelIdentityResourceImport Reconciler should import a LabelIdentity successfully but got error = %v", err)
		} else if !reflect.DeepEqual(*labelIdentityResImport.Spec.LabelIdentity, labelIdentity.Spec) {
			t.Errorf("LabelIdentityResourceImport Reconciler imported a LabelIdentity incorrectly. Exp: %v, Act: %v", labelIdentityResImport.Spec.LabelIdentity, labelIdentity.Spec)
		}
	}
}

func TestLabelIdentityResourceImportReconciler_handleLabelIdentityUpdateEvent(t *testing.T) {
	remoteMgr := NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	existLabelIdentity := &mcsv1alpha1.LabelIdentity{
		ObjectMeta: metav1.ObjectMeta{
			Name: labelIdentityResImportName,
		},
		Spec: mcsv1alpha1.LabelIdentitySpec{
			Label: "namespace:kubernetes.io/metadata.name=ns&pod:app=client",
			ID:    uint32(2),
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existLabelIdentity).Build()
	fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(labelIdentityResImport).Build()
	remoteCluster := NewFakeRemoteCommonArea(scheme, remoteMgr, fakeRemoteClient, "leader-cluster", "default")
	r := NewLabelIdentityResourceImportReconciler(fakeClient, scheme, fakeClient, localClusterID, "default", remoteCluster)
	r.installedLabelImports.Add(*labelIdentityResImport)

	if _, err := r.Reconcile(ctx, labelIdentityResImpReq); err != nil {
		t.Errorf("LabelIdentityResourceImport Reconciler should handle LabelIdentity delete event successfully but got error = %v", err)
	} else {
		labelIdentity := &mcsv1alpha1.LabelIdentity{}
		if err := fakeClient.Get(ctx, types.NamespacedName{Namespace: "", Name: labelIdentityResImportName}, labelIdentity); err != nil {
			t.Errorf("LabelIdentityResourceImport Reconciler should import a LabelIdentity successfully but got error = %v", err)
		} else if !reflect.DeepEqual(labelIdentity.Spec, *labelIdentityResImport.Spec.LabelIdentity) {
			t.Errorf("LabelIdentityResourceImport Reconciler failed to update a LabelIdentity. Exp: %v, Act: %v", labelIdentityResImport.Spec.LabelIdentity, labelIdentity.Spec)
		}
	}
}

func TestLabelIdentityResourceImportReconciler_handleLabelIdentityDeleteEvent(t *testing.T) {
	remoteMgr := NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	existingLabelIdentity := &mcsv1alpha1.LabelIdentity{
		ObjectMeta: metav1.ObjectMeta{
			Name: labelIdentityResImportName,
		},
		Spec: mcsv1alpha1.LabelIdentitySpec{
			Label: "namespace:kubernetes.io/metadata.name=ns&pod:app=client",
			ID:    uint32(1),
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingLabelIdentity).Build()
	fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	remoteCluster := NewFakeRemoteCommonArea(scheme, remoteMgr, fakeRemoteClient, "leader-cluster", "default")

	r := NewLabelIdentityResourceImportReconciler(fakeClient, scheme, fakeClient, localClusterID, "default", remoteCluster)
	r.installedLabelImports.Add(*labelIdentityResImport)

	if _, err := r.Reconcile(ctx, labelIdentityResImpReq); err != nil {
		t.Errorf("LabelIdentityResourceImport Reconciler should handle LabelIdentity update event successfully but got error = %v", err)
	} else {
		labelIdentity := &mcsv1alpha1.LabelIdentity{}
		if err := fakeClient.Get(ctx, types.NamespacedName{Namespace: "", Name: labelIdentityResImportName}, labelIdentity); !apierrors.IsNotFound(err) {
			t.Errorf("LabelIdentityResourceImport Reconciler failed to delete a LabelIdentity")
		}
	}
}
