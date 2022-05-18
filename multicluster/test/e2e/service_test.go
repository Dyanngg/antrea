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

package e2e

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	crdv1alpha1 "antrea.io/antrea/pkg/apis/crd/v1alpha1"
	antreae2e "antrea.io/antrea/test/e2e"
	e2euttils "antrea.io/antrea/test/e2e/utils"
)

var nodeNames map[string]string

func testServiceExport(t *testing.T, data *MCTestData) {
	data.testServiceExport(t)
}

// testServiceExport is used to test the connectivity of Multicluster Service between
// member clusters. We create a nginx Pod and Service in one member cluster, and try to
// curl it from a Pod in another cluster. If we get status code 200, it means that the
// resources are exported and imported successfully in each member cluster.
func (data *MCTestData) testServiceExport(t *testing.T) {
	podName := randName("test-nginx-")
	clientPodName := "test-service-client"

	if err := createPodWrapper(t, data, westCluster, multiClusterTestNamespace, podName, nginxImage, "nginx", nil, nil, nil, nil, false, nil); err != nil {
		t.Fatalf("Error when creating nginx Pod in west cluster: %v", err)
	}
	defer deletePodWrapper(t, data, westCluster, multiClusterTestNamespace, podName)

	if err := createPodWrapper(t, data, eastCluster, multiClusterTestNamespace, podName, nginxImage, "nginx", nil, nil, nil, nil, false, nil); err != nil {
		t.Fatalf("Error when creating nginx Pod in east cluster: %v", err)
	}
	defer deletePodWrapper(t, data, eastCluster, multiClusterTestNamespace, podName)

	if _, err := data.createService(westCluster, westClusterTestService, multiClusterTestNamespace, 80, 80, corev1.ProtocolTCP, map[string]string{"app": "nginx"}, false,
		false, corev1.ServiceTypeClusterIP, nil, nil); err != nil {
		t.Fatalf("Error when creating Service %s in west cluster: %v", westClusterTestService, err)
	}
	defer deleteServiceWrapper(t, testData, westCluster, multiClusterTestNamespace, westClusterTestService)

	if _, err := data.createService(eastCluster, eastClusterTestService, multiClusterTestNamespace, 80, 80, corev1.ProtocolTCP, map[string]string{"app": "nginx"}, false,
		false, corev1.ServiceTypeClusterIP, nil, nil); err != nil {
		t.Fatalf("Error when creating Service %s in east cluster: %v", eastClusterTestService, err)
	}
	defer deleteServiceWrapper(t, testData, eastCluster, multiClusterTestNamespace, eastClusterTestService)

	if err := data.deployServiceExport(westCluster); err != nil {
		t.Fatalf("Error when deploy ServiceExport in west cluster: %v", err)
	}
	defer data.deleteServiceExport(westCluster)
	if err := data.deployServiceExport(eastCluster); err != nil {
		t.Fatalf("Error when deploy ServiceExport in east cluster: %v", err)
	}
	defer data.deleteServiceExport(eastCluster)
	time.Sleep(importServiceDelay)

	// Create a Pod in east cluster and verify the MC Service connectivity from it.
	if err := data.createPod(eastCluster, clientPodName, multiClusterTestNamespace, "client", agnhostImage,
		[]string{"sleep", strconv.Itoa(3600)}, nil, nil, nil, false, nil); err != nil {
		t.Fatalf("Error when creating client Pod in east cluster: %v", err)
	}
	defer deletePodWrapper(t, data, eastCluster, multiClusterTestNamespace, clientPodName)
	_, err := data.podWaitFor(defaultTimeout, eastCluster, clientPodName, multiClusterTestNamespace, func(pod *corev1.Pod) (bool, error) {
		return pod.Status.Phase == corev1.PodRunning, nil
	})
	if err != nil {
		t.Fatalf("Error when waiting for Pod '%s' in east cluster: %v", clientPodName, err)
	}

	svc, err := data.getService(eastCluster, multiClusterTestNamespace, fmt.Sprintf("antrea-mc-%s", westClusterTestService))
	if err != nil {
		t.Fatalf("Error when getting the imported service %s: %v", fmt.Sprintf("antrea-mc-%s", westClusterTestService), err)
	}

	eastIP := svc.Spec.ClusterIP
	if err := data.probeServiceFromPodInCluster(eastCluster, clientPodName, "client", multiClusterTestNamespace, eastIP); err != nil {
		t.Fatalf("Error when probing service from %s, err: %v", eastCluster, err)
	}

	// Create a Pod in west cluster and verify the MC Service connectivity from it.
	if err := data.createPod(westCluster, clientPodName, multiClusterTestNamespace, "client", agnhostImage,
		[]string{"sleep", strconv.Itoa(3600)}, nil, nil, nil, false, nil); err != nil {
		t.Fatalf("Error when creating client Pod in west cluster: %v", err)
	}
	defer deletePodWrapper(t, data, westCluster, multiClusterTestNamespace, clientPodName)
	_, err = data.podWaitFor(defaultTimeout, westCluster, clientPodName, multiClusterTestNamespace, func(pod *corev1.Pod) (bool, error) {
		return pod.Status.Phase == corev1.PodRunning, nil
	})
	if err != nil {
		t.Fatalf("Error when waiting for Pod '%s' in west cluster: %v", clientPodName, err)
	}

	svc, err = data.getService(westCluster, multiClusterTestNamespace, fmt.Sprintf("antrea-mc-%s", eastClusterTestService))
	if err != nil {
		t.Fatalf("Error when getting the imported service %s: %v", fmt.Sprintf("antrea-mc-%s", eastClusterTestService), err)
	}
	westIP := svc.Spec.ClusterIP
	if err := data.probeServiceFromPodInCluster(westCluster, clientPodName, "client", multiClusterTestNamespace, westIP); err != nil {
		t.Fatalf("Error when probing service from %s, err: %v", westCluster, err)
	}

	// Verify that ACNP works fine with new Multicluster Service.
	data.verifyMCServiceACNP(t, clientPodName, eastIP)
}

func (data *MCTestData) verifyMCServiceACNP(t *testing.T, clientPodName, eastIP string) {
	var err error
	anpBuilder := &e2euttils.AntreaNetworkPolicySpecBuilder{}
	anpBuilder = anpBuilder.SetName(multiClusterTestNamespace, "block-west-exported-service").
		SetPriority(1.0).
		SetAppliedToGroup([]e2euttils.ANPAppliedToSpec{{PodSelector: map[string]string{"app": "client"}}}).
		AddToServicesRule([]crdv1alpha1.NamespacedName{{
			Name:      fmt.Sprintf("antrea-mc-%s", westClusterTestService),
			Namespace: multiClusterTestNamespace},
		}, "", nil, crdv1alpha1.RuleActionDrop)
	if _, err := data.createOrUpdateANP(eastCluster, anpBuilder.Get()); err != nil {
		t.Fatalf("Error creating ANP %s: %v", anpBuilder.Name, err)
	}
	defer data.deleteANP(eastCluster, multiClusterTestNamespace, anpBuilder.Name)

	connectivity := data.probeFromPodInCluster(eastCluster, multiClusterTestNamespace, clientPodName, "client", eastIP, fmt.Sprintf("antrea-mc-%s", westClusterTestService), 80, corev1.ProtocolTCP)
	if connectivity == antreae2e.Error {
		t.Errorf("Failure -- could not complete probeFromPodInCluster: %v", err)
	} else if connectivity != antreae2e.Dropped {
		t.Errorf("Failure -- wrong result from probing exported Service after applying toService AntreaNetworkPolicy. Expected: %v, Actual: %v", antreae2e.Dropped, connectivity)
	}
}

func (data *MCTestData) deployServiceExport(clusterName string) error {
	rc, _, stderr, err := provider.RunCommandOnNode(clusterName, fmt.Sprintf("kubectl apply -f %s", serviceExportYML))
	if err != nil || rc != 0 || stderr != "" {
		return fmt.Errorf("error when deploying the ServiceExport: %v, stderr: %s", err, stderr)
	}

	return nil
}

func (data *MCTestData) deleteServiceExport(clusterName string) error {
	rc, _, stderr, err := provider.RunCommandOnNode(clusterName, fmt.Sprintf("kubectl delete -f %s", serviceExportYML))
	if err != nil || rc != 0 || stderr != "" {
		return fmt.Errorf("error when deleting the ServiceExport: %v, stderr: %s", err, stderr)
	}

	return nil
}

// getNodeName will pick up a Node randomly from Node list
func getNodeName(nodeName string) (string, error) {
	rc, output, stderr, err := provider.RunCommandOnNode(nodeName, "kubectl get node -o custom-columns=:metadata.name --no-headers")
	if err != nil || rc != 0 || stderr != "" {
		return "", fmt.Errorf("error when getting Node list: %v, stderr: %s", err, stderr)
	}
	nodes := strings.Split(output, "\n")
	source := rand.NewSource(time.Now().UnixNano())
	random := rand.New(source) // #nosec G404: for test only
	return nodes[random.Intn(3)], nil
}

// annotateGatewayNode adds an annotation to assign it as Gateway Node.
func (data *MCTestData) annotateGatewayNode(clusterName string, nodeName string) error {
	rc, _, stderr, err := provider.RunCommandOnNode(clusterName, fmt.Sprintf("kubectl annotate node %s multicluster.antrea.io/gateway=true", nodeName))
	if err != nil || rc != 0 || stderr != "" {
		return fmt.Errorf("error when annotate the Node %s: %s, stderr: %s", nodeName, err, stderr)
	}
	log.Infof("The Node %s is annotated as Gateway in cluster %s", nodeName, clusterName)
	return nil
}

func (data *MCTestData) deleteAnnotation(clusterName string, nodeName string) error {
	rc, _, stderr, err := provider.RunCommandOnNode(clusterName, fmt.Sprintf("kubectl annotate node %s multicluster.antrea.io/gateway-", nodeName))
	if err != nil || rc != 0 || stderr != "" {
		return fmt.Errorf("error when cleaning up annotation of the Node: %v, stderr: %s", err, stderr)
	}
	return nil
}

func initializeGateway(t *testing.T, data *MCTestData) {
	nodeNames = make(map[string]string)
	// Annotate a Node as Gateway, then member controller will create a Gateway correspondingly.
	for clusterName := range data.clusterTestDataMap {
		if clusterName == leaderCluster {
			// Skip Gateway initilization for the leader cluster
			continue
		}
		name, err := getNodeName(clusterName)
		failOnError(err, t)
		err = data.annotateGatewayNode(clusterName, name)
		failOnError(err, t)
		nodeNames[clusterName] = name
	}
}

func teardownGateway(t *testing.T, data *MCTestData) {
	for clusterName := range data.clusterTestDataMap {
		if clusterName == leaderCluster {
			continue
		}
		if _, ok := nodeNames[clusterName]; ok {
			if err := data.deleteAnnotation(clusterName, nodeNames[clusterName]); err != nil {
				log.Errorf("Error: %v", err)
			}
		}
	}
}
