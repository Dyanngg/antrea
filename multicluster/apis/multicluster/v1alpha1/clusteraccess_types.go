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

// ClusterAccess controls which peer member clusters in the ClusterSet can
// connect or import resources into the current cluster.
type ClusterAccess struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ClusterAccessSpec `json:"spec,omitempty"`
}

//+kubebuilder:object:root=true

type ClusterAccessList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterAccess `json:"items"`
}

type ClusterAccessSpec struct {
	AllowedList []ClusterAccessRule `json:"allowedList,omitempty"`
}

type ClusterAccessRule struct {
	// An empty ClusterSelector selects all member clusters in the ClusterSet.
	Clusters         ClusterSelector `json:"clusters,omitempty"`
	AllowedResources []ResourceType  `json:"allowedResources,omitempty"`
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
		&ClusterAccess{},
		&ClusterAccessList{},
	)
}
