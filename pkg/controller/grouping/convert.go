// Copyright 2021 Antrea Authors
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

package grouping

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog"

	secv1a1 "github.com/vmware-tanzu/antrea/pkg/apis/security/v1alpha1"
)

func statusErrorWithMessage(msg string, params ...interface{}) metav1.Status {
	return metav1.Status{
		Message: fmt.Sprintf(msg, params...),
		Status:  metav1.StatusFailure,
	}
}

func ConvertClusterGroupCRD(Object *unstructured.Unstructured, toVersion string) (*unstructured.Unstructured, metav1.Status) {
	klog.V(2).Info("Converting CRD for ClusterGroup %s", Object.GetName())
	convertedObject := Object.DeepCopy()
	fromVersion := Object.GetAPIVersion()
	if toVersion == fromVersion {
		return nil, statusErrorWithMessage("conversion from a version to itself should not call the webhook: %s", toVersion)
	}
	switch Object.GetAPIVersion() {
	case "core.antrea.tanzu.vmware.com/v1alpha2":
		switch toVersion {
		case "core.antrea.tanzu.vmware.com/v1alpha3":
			ipbObj, ok := convertedObject.Object["ipBlock"]
			if ok {
				delete(convertedObject.Object, "ipBlock")
				ipb := ipbObj.(secv1a1.IPBlock)
				ipBlocks := []secv1a1.IPBlock{ipb}
				convertedObject.Object["ipBlocks"] = ipBlocks
			}
		default:
			return nil, statusErrorWithMessage("unexpected conversion version %q", toVersion)
		}
	//case "core.antrea.tanzu.vmware.com/v1alpha3":
	//	switch toVersion {
	//	case "core.antrea.tanzu.vmware.com/v1alpha2":
	//		host, hasHost := convertedObject.Object["host"]
	//		port, hasPort := convertedObject.Object["port"]
	//		if hasHost || hasPort {
	//			if !hasHost {
	//				host = ""
	//			}
	//			if !hasPort {
	//				port = ""
	//			}
	//			convertedObject.Object["hostPort"] = fmt.Sprintf("%s:%s", host, port)
	//			delete(convertedObject.Object, "host")
	//			delete(convertedObject.Object, "port")
	//		}
	//	default:
	//		return nil, statusErrorWithMessage("unexpected conversion version %q", toVersion)
	//	}
	default:
		return nil, statusErrorWithMessage("unexpected conversion version %q", fromVersion)
	}
	return convertedObject, metav1.Status{
		Status: metav1.StatusSuccess,
	}
}