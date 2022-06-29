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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
)

var (
	podReq = ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: "ns",
		Name:      "pod-a",
	}}

	podA = &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "pod-a",
			Labels: map[string]string{
				"app": "client",
			},
		},
	}

	ns = &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ns",
			Labels: map[string]string{
				"kubernetes.io/metadata.name": "ns",
			},
		},
	}

	podNamespacedName = types.NamespacedName{Namespace: "ns", Name: "pod-a"}.String()
)

func TestLabelIdentityReconciler_handlePodAddEvent(t *testing.T) {
	remoteMgr := commonarea.NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(podA, ns).Build()
	fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects().Build()

	_ = commonarea.NewFakeRemoteCommonArea(scheme, remoteMgr, fakeRemoteClient, "leader-cluster", leaderNamespace)
	mcReconciler := NewMemberClusterSetReconciler(fakeClient, scheme, "default")
	mcReconciler.SetRemoteCommonAreaManager(remoteMgr)
	commonAreaGetter := mcReconciler
	commonArea, localClusterID, _ := commonAreaGetter.GetRemoteCommonAreaAndLocalID()
	r := NewLabelIdentityReconciler(fakeClient, scheme, commonAreaGetter)

	if _, err := r.Reconcile(ctx, podReq); err != nil {
		t.Errorf("LabelIdentity Reconciler got error during reconciling. error = %v", err)
	} else {
		if podLabelIdentity, ok := r.podLabelCache[podNamespacedName]; !ok || podLabelIdentity != normalizedLabelNSAppClient {
			t.Errorf("LabelIdentity Reconciler failed to store %s in r.podLabelCache[%s]", normalizedLabelNSAppClient, podNamespacedName)
		}
		if podSet, ok := r.labelToPodsCache[normalizedLabelNSAppClient]; !ok || !podSet.Has(podNamespacedName) {
			t.Errorf("LabelIdentity Reconciler failed to store %s in r.labelToPodsCache[%s]", podNamespacedName, normalizedLabelNSAppClient)
		}
		labelIdentityExport := &mcsv1alpha1.ResourceExport{}
		err := commonArea.Get(ctx, types.NamespacedName{Namespace: commonArea.GetNamespace(), Name: getResourceExportNameForLabelIdentity(localClusterID)}, labelIdentityExport)
		if err != nil {
			t.Errorf("LabelIdentity Reconciler should create new LabelIdentityExport successfully but got error = %v", err)
		} else {
			for _, normalizedLabel := range labelIdentityExport.Spec.LabelIdentities.NormalizedLabels {
				if normalizedLabel == normalizedLabelNSAppClient {
					return
				}
			}
			t.Errorf("LabelIdentity Reconciler create LabelIdentityExport incorrectly. Should include %s, ActLabels:%s", normalizedLabelNSAppClient, labelIdentityExport.Spec.LabelIdentities.NormalizedLabels)
		}
	}
}

func TestLabelIdentityReconciler_handlePodUpdateEvent(t *testing.T) {
	remoteMgr := commonarea.NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	newPodA := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "pod-a",
			Labels: map[string]string{
				"app": "db",
			},
		},
	}

	normalizedLabelNSAppDB := "namespace:kubernetes.io/metadata.name=ns&pod:app=db"

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(podA, ns).Build()
	fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects().Build()

	_ = commonarea.NewFakeRemoteCommonArea(scheme, remoteMgr, fakeRemoteClient, "leader-cluster", leaderNamespace)
	mcReconciler := NewMemberClusterSetReconciler(fakeClient, scheme, "default")
	mcReconciler.SetRemoteCommonAreaManager(remoteMgr)
	commonAreaGetter := mcReconciler
	commonArea, localClusterID, _ := commonAreaGetter.GetRemoteCommonAreaAndLocalID()
	r := NewLabelIdentityReconciler(fakeClient, scheme, commonAreaGetter)
	if _, err := r.Reconcile(ctx, podReq); err != nil {
		t.Errorf("LabelIdentity Reconciler got error during reconciling. error = %v", err)
	}

	r.Client.Update(ctx, newPodA, &client.UpdateOptions{})
	if _, err := r.Reconcile(ctx, podReq); err != nil {
		t.Errorf("LabelIdentity Reconciler got error during reconciling. error = %v", err)
	} else {
		if podLabelIdentity, ok := r.podLabelCache[podNamespacedName]; !ok || podLabelIdentity != normalizedLabelNSAppDB {
			t.Errorf("LabelIdentity Reconciler failed to store %s in r.podLabelCache[%s]", normalizedLabelNSAppDB, podNamespacedName)
		}
		if podSet, ok := r.labelToPodsCache[normalizedLabelNSAppDB]; !ok || !podSet.Has(podNamespacedName) {
			t.Errorf("LabelIdentity Reconciler failed to store %s in r.labelToPodsCache[%s]", podNamespacedName, normalizedLabelNSAppDB)
		}
		if podSet, ok := r.labelToPodsCache[normalizedLabelNSAppClient]; ok && !podSet.Has(podNamespacedName) {
			t.Errorf("LabelIdentity Reconciler failed to delete %s in r.labelToPodsCache[%s]", podNamespacedName, normalizedLabelNSAppClient)
		}
		labelIdentityExport := &mcsv1alpha1.ResourceExport{}
		err := commonArea.Get(ctx, types.NamespacedName{Namespace: commonArea.GetNamespace(), Name: getResourceExportNameForLabelIdentity(localClusterID)}, labelIdentityExport)
		if err != nil {
			t.Errorf("LabelIdentity Reconciler should create new LabelIdentityExport successfully but got error = %v", err)
		} else {
			findDB := false
			findClient := false
			for _, normalizedLabel := range labelIdentityExport.Spec.LabelIdentities.NormalizedLabels {
				if normalizedLabel == normalizedLabelNSAppDB {
					findDB = true
				}
				if normalizedLabel == normalizedLabelNSAppClient {
					findClient = true
				}
			}
			if !findDB || findClient {
				t.Errorf("LabelIdentity Reconciler create LabelIdentityExport incorrectly. Should include %s and not include %s, ActLabels:%s", normalizedLabelNSAppDB, normalizedLabelNSAppClient, labelIdentityExport.Spec.LabelIdentities.NormalizedLabels)
			}
		}
	}
}

func TestLabelIdentityReconciler_handlePodDeleteEvent(t *testing.T) {
	remoteMgr := commonarea.NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(podA, ns).Build()
	fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects().Build()

	_ = commonarea.NewFakeRemoteCommonArea(scheme, remoteMgr, fakeRemoteClient, "leader-cluster", leaderNamespace)
	mcReconciler := NewMemberClusterSetReconciler(fakeClient, scheme, "default")
	mcReconciler.SetRemoteCommonAreaManager(remoteMgr)
	commonAreaGetter := mcReconciler
	commonArea, localClusterID, _ := commonAreaGetter.GetRemoteCommonAreaAndLocalID()
	r := NewLabelIdentityReconciler(fakeClient, scheme, commonAreaGetter)

	if _, err := r.Reconcile(ctx, podReq); err != nil {
		t.Errorf("LabelIdentity Reconciler got error during reconciling. error = %v", err)
	}

	r.Client.Delete(ctx, podA, &client.DeleteOptions{})
	if _, err := r.Reconcile(ctx, podReq); err != nil {
		t.Errorf("LabelIdentity Reconciler got error during reconciling. error = %v", err)
	} else {
		if _, ok := r.podLabelCache[podNamespacedName]; ok {
			t.Errorf("LabelIdentity Reconciler failed to delete r.podLabelCache[%s]", podNamespacedName)
		}
		if podSet, ok := r.labelToPodsCache[normalizedLabelNSAppClient]; ok && podSet.Has(podNamespacedName) {
			t.Errorf("LabelIdentity Reconciler failed to delete %s in r.labelToPodsCache[%s]", podNamespacedName, normalizedLabelNSAppClient)
		}
		labelIdentityExport := &mcsv1alpha1.ResourceExport{}
		err := commonArea.Get(ctx, types.NamespacedName{Namespace: commonArea.GetNamespace(), Name: getResourceExportNameForLabelIdentity(localClusterID)}, labelIdentityExport)
		if err != nil {
			t.Errorf("LabelIdentity Reconciler should create new LabelIdentityExport successfully but got error = %v", err)
		} else {
			for _, normalizedLabel := range labelIdentityExport.Spec.LabelIdentities.NormalizedLabels {
				if normalizedLabel == normalizedLabelNSAppClient {
					t.Errorf("LabelIdentity Reconciler create LabelIdentityExport incorrectly. Should not include %s, ActLabels:%s", normalizedLabelNSAppClient, labelIdentityExport.Spec.LabelIdentities.NormalizedLabels)
					break
				}
			}
		}
	}
}

func TestLabelIdentityReconciler_handleNSUpdateEvent(t *testing.T) {
	remoteMgr := commonarea.NewRemoteCommonAreaManager("test-clusterset", common.ClusterID(localClusterID), "kube-system")
	remoteMgr.Start()
	defer remoteMgr.Stop()

	nsReq := ctrl.Request{NamespacedName: types.NamespacedName{
		Name: "ns",
	}}

	newNS := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ns",
			Labels: map[string]string{
				"kubernetes.io/metadata.name": "ns",
				"level":                       "admin",
			},
		},
	}

	normalizedLabelNSAdminAppClient := "namespace:kubernetes.io/metadata.name=ns,level=admin&pod:app=client"

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(podA, ns).Build()
	fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects().Build()

	_ = commonarea.NewFakeRemoteCommonArea(scheme, remoteMgr, fakeRemoteClient, "leader-cluster", leaderNamespace)
	mcReconciler := NewMemberClusterSetReconciler(fakeClient, scheme, "default")
	mcReconciler.SetRemoteCommonAreaManager(remoteMgr)
	commonAreaGetter := mcReconciler
	commonArea, localClusterID, _ := commonAreaGetter.GetRemoteCommonAreaAndLocalID()
	r := NewLabelIdentityReconciler(fakeClient, scheme, commonAreaGetter)
	if _, err := r.Reconcile(ctx, podReq); err != nil {
		t.Errorf("LabelIdentity Reconciler got error during reconciling. error = %v", err)
	}

	r.Client.Update(ctx, newNS, &client.UpdateOptions{})
	if _, err := r.Reconcile(ctx, nsReq); err != nil {
		t.Errorf("LabelIdentity Reconciler got error during reconciling. error = %v", err)
	} else {
		if podLabelIdentity, ok := r.podLabelCache[podNamespacedName]; !ok || podLabelIdentity != normalizedLabelNSAdminAppClient {
			t.Errorf("LabelIdentity Reconciler failed to store %s in r.podLabelCache[%s]", normalizedLabelNSAdminAppClient, podNamespacedName)
		}
		if podSet, ok := r.labelToPodsCache[normalizedLabelNSAdminAppClient]; !ok || !podSet.Has(podNamespacedName) {
			t.Errorf("LabelIdentity Reconciler failed to store %s in r.labelToPodsCache[%s]", podNamespacedName, normalizedLabelNSAdminAppClient)
		}
		if podSet, ok := r.labelToPodsCache[normalizedLabelNSAppClient]; ok && !podSet.Has(podNamespacedName) {
			t.Errorf("LabelIdentity Reconciler failed to delete %s in r.labelToPodsCache[%s]", podNamespacedName, normalizedLabelNSAppClient)
		}
		labelIdentityExport := &mcsv1alpha1.ResourceExport{}
		err := commonArea.Get(ctx, types.NamespacedName{Namespace: commonArea.GetNamespace(), Name: getResourceExportNameForLabelIdentity(localClusterID)}, labelIdentityExport)
		if err != nil {
			t.Errorf("LabelIdentity Reconciler should create new LabelIdentityExport successfully but got error = %v", err)
		} else {
			findAdminClient := false
			findClient := false
			for _, normalizedLabel := range labelIdentityExport.Spec.LabelIdentities.NormalizedLabels {
				if normalizedLabel == normalizedLabelNSAdminAppClient {
					findAdminClient = true
				}
				if normalizedLabel == normalizedLabelNSAppClient {
					findClient = true
				}
			}
			if !findAdminClient || findClient {
				t.Errorf("LabelIdentity Reconciler create LabelIdentityExport incorrectly. Should include %s and not include %s, ActLabels:%s", normalizedLabelNSAdminAppClient, normalizedLabelNSAppClient, labelIdentityExport.Spec.LabelIdentities.NormalizedLabels)
			}
		}
	}
}
