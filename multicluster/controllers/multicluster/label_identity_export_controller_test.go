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
	mcsv1alpha1 "antrea.io/antrea/multicluster/apis/multicluster/v1alpha1"
	"antrea.io/antrea/multicluster/controllers/multicluster/common"
	"antrea.io/antrea/multicluster/controllers/multicluster/commonarea"
	"reflect"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	normalizedLabelNSAppClient = "namespace:kubernetes.io/metadata.name=ns&pod:app=client"

	labelIdentityExportReq = ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: "default",
		Name:      "label-identity-app-client",
	}}

	labelIdentityExport = &mcsv1alpha1.LabelIdentityExport{
		NormalizedLabels: []string{
			normalizedLabelNSAppClient,
		},
	}

	labelIdentityResExport = &mcsv1alpha1.ResourceExport{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "label-identity-app-client",
		},
		Spec: mcsv1alpha1.ResourceExportSpec{
			ClusterID:       localClusterID,
			LabelIdentities: labelIdentityExport,
		},
	}
)

func TestLabelIdentityExportReconciler_handleCreateEvent(t *testing.T) {
	remoteMgr := commonarea.NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(labelIdentityResExport).Build()
	r := NewLabelIdentityExportReconciler(fakeClient, scheme)

	if _, err := r.Reconcile(ctx, labelIdentityExportReq); err != nil {
		t.Errorf("LabelIdentityExport Reconciler got error during reconciling. error = %v", err)
	} else {
		if clusterIDSet, ok := r.labelsToClusters[normalizedLabelNSAppClient]; !ok || !clusterIDSet.Has(localClusterID) {
			t.Errorf("LabelIdentityExport Reconciler failed to store %s in r.labelsToClusters[%s]", localClusterID, normalizedLabelNSAppClient)
		}
		if labelsSet, ok := r.clusterToLabels[localClusterID]; !ok || !labelsSet.Has(normalizedLabelNSAppClient) {
			t.Errorf("LabelIdentityExport Reconciler failed to store %s in r.clusterToLabels[%s]", normalizedLabelNSAppClient, localClusterID)
		}
		labelIdentityImport := &mcsv1alpha1.LabelIdentityImport{}
		err := fakeClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: hashLabelIdentity(normalizedLabelNSAppClient)}, labelIdentityImport)
		if err != nil {
			t.Errorf("LabelIdentityExport Reconciler should create new LabelIdentityImport successfully but got error = %v", err)
		} else if labelIdentityImport.Spec.Label != normalizedLabelNSAppClient {
			t.Errorf("LabelIdentityExport Reconciler create LabelIdentityImport incorrectly. ExpLabel:%s, ActLabel:%s", normalizedLabelNSAppClient, labelIdentityImport.Spec.Label)
		}
	}
}

func TestLabelIdentityExportReconciler_handleUpdateEvent(t *testing.T) {
	remoteMgr := commonarea.NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	normalizedLabelNSAppDB := "namespace:kubernetes.io/metadata.name=ns&pod:app=db"

	newLabelIdentityExportReq := ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: "default",
		Name:      "label-identity-app-db",
	}}
	newLabelIdentityExport := &mcsv1alpha1.LabelIdentityExport{
		NormalizedLabels: []string{
			normalizedLabelNSAppDB,
		},
	}
	newLabelIdentityResExport := &mcsv1alpha1.ResourceExport{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "label-identity-app-db",
		},
		Spec: mcsv1alpha1.ResourceExportSpec{
			ClusterID:       localClusterID,
			LabelIdentities: newLabelIdentityExport,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(labelIdentityResExport, newLabelIdentityResExport).Build()
	r := NewLabelIdentityExportReconciler(fakeClient, scheme)
	if _, err := r.Reconcile(ctx, labelIdentityExportReq); err != nil {
		t.Errorf("LabelIdentityExport Reconciler got error during reconciling. error = %v", err)
	}

	if _, err := r.Reconcile(ctx, newLabelIdentityExportReq); err != nil {
		t.Errorf("LabelIdentityExport Reconciler got error during reconciling. error = %v", err)
	} else {
		if clusterIDSet, ok := r.labelsToClusters[normalizedLabelNSAppClient]; ok && clusterIDSet.Has(localClusterID) {
			t.Errorf("LabelIdentityExport Reconciler failed to delete %s in r.labelsToClusters[%s]", localClusterID, normalizedLabelNSAppClient)
		}
		if labelsSet, ok := r.clusterToLabels[localClusterID]; ok && labelsSet.Has(normalizedLabelNSAppClient) {
			t.Errorf("LabelIdentityExport Reconciler failed to delete %s in r.clusterToLabels[%s]", normalizedLabelNSAppClient, localClusterID)
		}
		if clusterIDSet, ok := r.labelsToClusters[normalizedLabelNSAppDB]; !ok || !clusterIDSet.Has(localClusterID) {
			t.Errorf("LabelIdentityExport Reconciler failed to store %s in r.labelsToClusters[%s]", localClusterID, normalizedLabelNSAppDB)
		}
		if labelsSet, ok := r.clusterToLabels[localClusterID]; !ok || !labelsSet.Has(normalizedLabelNSAppDB) {
			t.Errorf("LabelIdentityExport Reconciler failed to store %s in r.clusterToLabels[%s]", normalizedLabelNSAppDB, localClusterID)
		}

		clientLabelIdentityImport := &mcsv1alpha1.LabelIdentityImport{}
		err := fakeClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: hashLabelIdentity(normalizedLabelNSAppClient)}, clientLabelIdentityImport)
		if !reflect.DeepEqual(clientLabelIdentityImport, &mcsv1alpha1.LabelIdentityImport{}) {
			t.Errorf("LabelIdentityExport Reconciler failed to delete LabelIdentityImport for label:%s", normalizedLabelNSAppClient)
		}

		dbLabelIdentityImport := &mcsv1alpha1.LabelIdentityImport{}
		err = fakeClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: hashLabelIdentity(normalizedLabelNSAppDB)}, dbLabelIdentityImport)
		if err != nil {
			t.Errorf("LabelIdentityExport Reconciler should create new LabelIdentityImport successfully but got error = %v", err)
		} else if dbLabelIdentityImport.Spec.Label != normalizedLabelNSAppDB {
			t.Errorf("LabelIdentityExport Reconciler create LabelIdentityImport incorrectly. ExpLabel:%s, ActLabel:%s", normalizedLabelNSAppClient, dbLabelIdentityImport.Spec.Label)
		}
	}
}
