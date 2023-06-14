/*
Copyright 2023 Antrea Authors.
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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=clusterimportcontrols

// ClusterImportControl controls which peer member clusters in the ClusterSet can
// connect or import resources into the appliedTo clusters.
type ClusterImportControl struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ClusterImportControlSpec `json:"spec,omitempty"`
}

//+kubebuilder:object:root=true

type ClusterImportControlList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterImportControl `json:"items"`
}

type ClusterImportControlSpec struct {
	AppliedToClusters []string                   `json:"appliedToClusters"`
	AllowedList       []ClusterImportControlRule `json:"allowedList,omitempty"`
}

type ClusterImportControlRule struct {
	// An empty ClusterSelector selects all member clusters in the ClusterSet.
	Clusters  ClusterSelector `json:"clusters,omitempty"`
	Resources []ResourceType  `json:"resources,omitempty"`
}

type ClusterSelector struct {
	ClusterIDs []string `json:"clusterIDs,omitempty"`
}

type ResourceType string

const (
	ResourceTypeConnectivity ResourceType = "Connectivity"
)

func init() {
	SchemeBuilder.Register(
		&ClusterImportControl{},
		&ClusterImportControlList{},
	)
}
