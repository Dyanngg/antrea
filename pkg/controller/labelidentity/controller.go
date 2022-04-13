// Copyright 2022 Antrea Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package labelidentity

import (
	"k8s.io/client-go/tools/cache"

	//multiclusterv1alpha1 "antrea.io/antrea/multicluster/apis/multicluster/v1alpha1"
	mcinformers "antrea.io/antrea/multicluster/pkg/client/informers/externalversions/multicluster/v1alpha1"
)

type LabelIdentityController struct {
	labelInformer mcinformers.LabelIdentityImportInformer
	// labelListerSynced is a function which returns true if the LabelIdentityImport shared informer
	// has been synced at least once
	labelListerSynced cache.InformerSynced
}

func (c *LabelIdentityController) addLabelIdentity(obj interface{}) {
	//labelIdentityImport := obj.(*multiclusterv1alpha1.LabelIdentityImport)
	//c.
}

