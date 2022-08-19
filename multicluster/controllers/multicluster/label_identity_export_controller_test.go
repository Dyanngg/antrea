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
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcsv1alpha1 "antrea.io/antrea/multicluster/apis/multicluster/v1alpha1"
	"antrea.io/antrea/multicluster/controllers/multicluster/common"
)

var (
	normalizedLabel = "namespace:kubernetes.io/metadata.name=ns&pod:app=client"
	labelHash       = common.HashLabelIdentity(normalizedLabel)

	resExpNamespacedName = types.NamespacedName{
		Namespace: leaderNamespace,
		Name:      localClusterID + "-" + labelHash,
	}

	labelIdentityResExp = &mcsv1alpha1.ResourceExport{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: leaderNamespace,
			Name:      localClusterID + "-" + labelHash,
		},
		Spec: mcsv1alpha1.ResourceExportSpec{
			ClusterID: localClusterID,
			LabelIdentity: &mcsv1alpha1.LabelIdentityExport{
				NormalizedLabel: normalizedLabel,
			},
		},
	}
)

func TestLabelIdentityResourceExportReconclie(t *testing.T) {
	tests := []struct {
		name                 string
		existResExp          *mcsv1alpha1.ResourceExport
		resExpNamespacedName types.NamespacedName
		expNormalizedLabel   string
		expLabelsToClusters  map[string]sets.String
		expClusterToLabels   map[string]sets.String
		isDelete             bool
	}{
		{
			"create LabelIdentity kind of ResImp",
			labelIdentityResExp,
			resExpNamespacedName,
			normalizedLabel,
			map[string]sets.String{labelHash: sets.NewString(localClusterID)},
			map[string]sets.String{localClusterID: sets.NewString(labelHash)},
			false,
		},
		{
			"delete LabelIdentity kind of ResImp",
			labelIdentityResExp,
			resExpNamespacedName,
			"",
			map[string]sets.String{},
			map[string]sets.String{localClusterID: sets.NewString()},
			true,
		},
	}

	for _, tt := range tests {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.existResExp).Build()
		r := NewLabelIdentityExportReconciler(fakeClient, scheme, leaderNamespace)

		resExpReq := ctrl.Request{NamespacedName: tt.resExpNamespacedName}
		if _, err := r.Reconcile(ctx, resExpReq); err != nil {
			t.Errorf("LabelIdentityExport Reconciler got error during reconciling. error = %v", err)
			continue
		}
		if tt.isDelete {
			fakeClient.Delete(ctx, tt.existResExp)
			if _, err := r.Reconcile(ctx, resExpReq); err != nil {
				t.Errorf("LabelIdentityExport Reconciler got error during reconciling. error = %v", err)
				continue
			}
		}

		if !reflect.DeepEqual(r.labelsToClusters, tt.expLabelsToClusters) {
			t.Errorf("LabelIdentityExport Reconciler operated labelsToClusters incorrectly. Exp: %s, Act: %s", tt.expLabelsToClusters, r.labelsToClusters)
		}
		if !reflect.DeepEqual(r.clusterToLabels, tt.expClusterToLabels) {
			t.Errorf("LabelIdentityExport Reconciler operated clusterToLabels incorrectly. Exp: %s, Act: %s", tt.expClusterToLabels, r.clusterToLabels)
		}

		actLabelIdentityResImp := &mcsv1alpha1.ResourceImport{}
		lastIdx := strings.LastIndex(tt.resExpNamespacedName.Name, "-")
		parsedLabelHash := tt.resExpNamespacedName.Name[lastIdx+1:]
		if err := fakeClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: parsedLabelHash}, actLabelIdentityResImp); err == nil {
			if tt.expNormalizedLabel != actLabelIdentityResImp.Spec.LabelIdentity.Label {
				t.Errorf("LabelIdentityExport Reconciler create LabelIdentity kind of ResourceImport incorrectly. ExpLabel:%s, ActLabel:%s", tt.expNormalizedLabel, actLabelIdentityResImp.Spec.LabelIdentity.Label)
			}
		} else {
			if tt.isDelete {
				if !apierrors.IsNotFound(err) {
					t.Errorf("LabelIdentityExport Reconciler expects not found error but got error = %v", err)
				}
			} else {
				t.Errorf("Expected a LabelIdentity kind of ResourceImport but got error = %v", err)
			}
		}
	}
}
