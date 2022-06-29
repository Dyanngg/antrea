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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcsv1alpha1 "antrea.io/antrea/multicluster/apis/multicluster/v1alpha1"
	"antrea.io/antrea/multicluster/controllers/multicluster/common"
)

var (
	labelIdentityImportName = "label-identity-app-client"

	labelIdentityImpReq = ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: leaderNamespace,
		Name:      labelIdentityImportName,
	}}

	labelIdentityImport = &mcsv1alpha1.LabelIdentityImport{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: leaderNamespace,
			Name:      labelIdentityImportName,
		},
		Spec: mcsv1alpha1.LabelIdentityImportSpec{
			Label: "namespace:kubernetes.io/metadata.name=ns&pod:app=client",
			ID:    uint32(1),
		},
	}
)

func TestLabelIdentityImportReconciler_handleLabelIdentityCreateEvent(t *testing.T) {
	remoteMgr := NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(labelIdentityImport).Build()
	remoteCluster := NewFakeRemoteCommonArea(scheme, remoteMgr, fakeRemoteClient, "leader-cluster", "default")

	r := NewLabelIdentityImportReconciler(fakeClient, scheme, fakeClient, localClusterID, "default", remoteCluster)

	if _, err := r.Reconcile(ctx, labelIdentityImpReq); err != nil {
		t.Errorf("LabelIdentityImport Reconciler should handle LabelIdentity create event successfully but got error = %v", err)
	} else {
		labelIdentity := &mcsv1alpha1.LabelIdentity{}
		if err := fakeClient.Get(ctx, types.NamespacedName{Namespace: "", Name: labelIdentityImportName}, labelIdentity); err != nil {
			t.Errorf("LabelIdentityImport Reconciler should import a LabelIdentity successfully but got error = %v", err)
		} else if !reflect.DeepEqual(labelIdentityImport.Spec, labelIdentity.Spec) {
			t.Errorf("LabelIdentityImport Reconciler imported a LabelIdentity incorrectly. Exp: %v, Act: %v", labelIdentityImport.Spec, labelIdentity.Spec)
		}
	}
}

func TestLabelIdentityImportReconciler_handleLabelIdentityUpdateEvent(t *testing.T) {
	remoteMgr := NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	existingLabelIdentity := &mcsv1alpha1.LabelIdentity{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: leaderNamespace,
			Name:      labelIdentityImportName,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingLabelIdentity).Build()
	fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	remoteCluster := NewFakeRemoteCommonArea(scheme, remoteMgr, fakeRemoteClient, "leader-cluster", "default")

	r := NewLabelIdentityImportReconciler(fakeClient, scheme, fakeClient, localClusterID, "default", remoteCluster)
	r.installedLabelImports.Add(*labelIdentityImport)

	if _, err := r.Reconcile(ctx, labelIdentityImpReq); err != nil {
		t.Errorf("LabelIdentityImport Reconciler should handle LabelIdentity delete event successfully but got error = %v", err)
	} else {
		labelIdentity := &mcsv1alpha1.LabelIdentity{}
		if err := fakeClient.Get(ctx, types.NamespacedName{Namespace: "", Name: labelIdentityImportName}, labelIdentity); !apierrors.IsNotFound(err) {
			t.Errorf("LabelIdentityImport Reconciler should import a LabelIdentity successfully but got error = %v", err)
		}
	}
}

func TestLabelIdentityImportReconciler_handleLabelIdentityDeleteEvent(t *testing.T) {
	remoteMgr := NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	existingLabelIdentity := &mcsv1alpha1.LabelIdentity{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: leaderNamespace,
			Name:      labelIdentityImportName,
		},
		Spec: mcsv1alpha1.LabelIdentityImportSpec{
			Label: "namespace:kubernetes.io/metadata.name=ns&pod:app=client",
			ID:    uint32(2),
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingLabelIdentity).Build()
	fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(labelIdentityImport).Build()
	remoteCluster := NewFakeRemoteCommonArea(scheme, remoteMgr, fakeRemoteClient, "leader-cluster", "default")

	r := NewLabelIdentityImportReconciler(fakeClient, scheme, fakeClient, localClusterID, "default", remoteCluster)
	r.installedLabelImports.Add(*labelIdentityImport)

	if _, err := r.Reconcile(ctx, labelIdentityImpReq); err != nil {
		t.Errorf("LabelIdentityImport Reconciler should handle LabelIdentity update event successfully but got error = %v", err)
	} else {
		labelIdentity := &mcsv1alpha1.LabelIdentity{}
		if err := fakeClient.Get(ctx, types.NamespacedName{Namespace: "", Name: labelIdentityImportName}, labelIdentity); err != nil {
			t.Errorf("LabelIdentityImport Reconciler should import a LabelIdentity successfully but got error = %v", err)
		} else if !reflect.DeepEqual(labelIdentityImport.Spec, labelIdentity.Spec) {
			t.Errorf("LabelIdentityImport Reconciler imported a LabelIdentity incorrectly. Exp: %v, Act: %v", labelIdentityImport.Spec, labelIdentity.Spec)
		}
	}
}
