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

var gatewayNames map[string]string
var regularNodeNames map[string][]string

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

	createPodAndService := func(clusterName, clusterServiceName string) {
		if err := createPodWrapper(t, data, clusterName, multiClusterTestNamespace, podName, "", nginxImage, "nginx", nil, nil, nil, nil, false, nil); err != nil {
			t.Fatalf("Error when creating nginx Pod in cluster %s: %v", clusterName, err)
		}
		if _, err := data.createService(clusterName, clusterServiceName, multiClusterTestNamespace, 80, 80, corev1.ProtocolTCP, map[string]string{"app": "nginx"}, false,
			false, corev1.ServiceTypeClusterIP, nil, nil); err != nil {
			t.Fatalf("Error when creating Service %s in cluster %s: %v", clusterServiceName, clusterName, err)
		}
	}
	// Create Pod and Service in west cluster
	createPodAndService(westCluster, westClusterTestService)
	defer deletePodWrapper(t, data, westCluster, multiClusterTestNamespace, podName)
	defer deleteServiceWrapper(t, testData, westCluster, multiClusterTestNamespace, westClusterTestService)

	// Create Pod and Service in east cluster
	createPodAndService(eastCluster, eastClusterTestService)
	defer deletePodWrapper(t, data, eastCluster, multiClusterTestNamespace, podName)
	defer deleteServiceWrapper(t, testData, eastCluster, multiClusterTestNamespace, eastClusterTestService)

	deployServiceExport := func(clusterName string) {
		if err := data.deployServiceExport(clusterName); err != nil {
			t.Fatalf("Error when deploy ServiceExport in cluster %s: %v", clusterName, err)
		}
	}

	// Deploy ServiceExport in west cluster
	deployServiceExport(westCluster)
	defer data.deleteServiceExport(westCluster)

	// Deploy ServiceExport in east cluster
	deployServiceExport(eastCluster)
	defer data.deleteServiceExport(eastCluster)
	time.Sleep(importServiceDelay)

	svc, err := data.getService(eastCluster, multiClusterTestNamespace, fmt.Sprintf("antrea-mc-%s", westClusterTestService))
	if err != nil {
		t.Fatalf("Error when getting the imported service %s: %v", fmt.Sprintf("antrea-mc-%s", westClusterTestService), err)
	}

	eastIP := svc.Spec.ClusterIP
	gwClientName := clientPodName + "-gateway"
	regularClientName := clientPodName + "-regularnode"

	createEastPod := func(nodeName string, podName string) {
		if err := data.createPod(eastCluster, podName, nodeName, multiClusterTestNamespace, "client", agnhostImage,
			[]string{"sleep", strconv.Itoa(3600)}, nil, nil, nil, false, nil); err != nil {
			t.Fatalf("Error when creating client Pod in east cluster: %v", err)
		}
		log.Infof("Checking Pod status %s in Namespace %s of cluster %s", podName, multiClusterTestNamespace, eastCluster)
		_, err := data.podWaitFor(defaultTimeout, eastCluster, podName, multiClusterTestNamespace, func(pod *corev1.Pod) (bool, error) {
			return pod.Status.Phase == corev1.PodRunning, nil
		})
		if err != nil {
			deletePodWrapper(t, data, eastCluster, multiClusterTestNamespace, podName)
			t.Fatalf("Error when waiting for Pod '%s' in east cluster: %v", podName, err)
		}
	}

	// Create a Pod in east cluster's Gateway and verify the MC Service connectivity from it.
	createEastPod(gatewayNames[eastCluster], gwClientName)
	defer deletePodWrapper(t, data, eastCluster, multiClusterTestNamespace, gwClientName)

	log.Infof("Probing Service from client Pod %s in cluster %s", gwClientName, eastCluster)
	if err := data.probeServiceFromPodInCluster(eastCluster, gwClientName, "client", multiClusterTestNamespace, eastIP); err != nil {
		t.Fatalf("Error when probing Service from client Pod %s in cluster %s, err: %v", gwClientName, eastCluster, err)
	}

	// Create a Pod in east cluster's regular Node and verify the MC Service connectivity from it.
	createEastPod(regularNodeNames[eastCluster][0], regularClientName)
	defer deletePodWrapper(t, data, eastCluster, multiClusterTestNamespace, regularClientName)

	log.Infof("Probing Service from client Pod %s in cluster %s", regularClientName, eastCluster)
	if err := data.probeServiceFromPodInCluster(eastCluster, regularClientName, "client", multiClusterTestNamespace, eastIP); err != nil {
		t.Fatalf("Error when probing Service from client Pod %s in cluster %s, err: %v", regularClientName, eastCluster, err)
	}

	// Create a Pod in west cluster and verify the MC Service connectivity from it.
	if err := data.createPod(westCluster, clientPodName, "", multiClusterTestNamespace, "client", agnhostImage,
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
	data.verifyMCServiceACNP(t, gwClientName, eastIP)
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
	rc, _, stderr, err := provider.RunCommandOnNode(clusterName, fmt.Sprintf("sudo kubectl apply -f %s", serviceExportYML))
	if err != nil || rc != 0 || stderr != "" {
		return fmt.Errorf("error when deploying the ServiceExport: %v, stderr: %s", err, stderr)
	}

	return nil
}

func (data *MCTestData) deleteServiceExport(clusterName string) error {
	rc, _, stderr, err := provider.RunCommandOnNode(clusterName, fmt.Sprintf("sudo kubectl delete -f %s", serviceExportYML))
	if err != nil || rc != 0 || stderr != "" {
		return fmt.Errorf("error when deleting the ServiceExport: %v, stderr: %s", err, stderr)
	}

	return nil
}

// getNodeNameAndList gets a Node name randomly as Gateway from Node list and a regular Node name list.
func getNodeNameAndList(nodeName string) (string, []string, error) {
	rc, output, stderr, err := provider.RunCommandOnNode(nodeName, "sudo kubectl get node -o custom-columns=:metadata.name --no-headers")
	if err != nil || rc != 0 || stderr != "" {
		return "", nil, fmt.Errorf("error when getting Node list: %v, stderr: %s", err, stderr)
	}
	nodes := strings.Split(output, "\n")
	source := rand.NewSource(time.Now().UnixNano())
	random := rand.New(source) // #nosec G404: for test only
	gwIdx := random.Intn(3)
	var regularNodes []string
	for i, node := range nodes {
		if i != gwIdx {
			regularNodes = append(regularNodes, node)
		}
	}
	return nodes[gwIdx], regularNodes, nil
}

// annotateGatewayNode adds an annotation to assign it as Gateway Node.
func (data *MCTestData) annotateGatewayNode(clusterName string, nodeName string) error {
	rc, _, stderr, err := provider.RunCommandOnNode(clusterName, fmt.Sprintf("sudo kubectl annotate node %s multicluster.antrea.io/gateway=true", nodeName))
	if err != nil || rc != 0 || stderr != "" {
		return fmt.Errorf("error when annotate the Node %s: %s, stderr: %s", nodeName, err, stderr)
	}
	log.Infof("The Node %s is annotated as Gateway in cluster %s", nodeName, clusterName)
	return nil
}

func (data *MCTestData) deleteAnnotation(clusterName string, nodeName string) error {
	rc, _, stderr, err := provider.RunCommandOnNode(clusterName, fmt.Sprintf("sudo kubectl annotate node %s multicluster.antrea.io/gateway-", nodeName))
	if err != nil || rc != 0 || stderr != "" {
		return fmt.Errorf("error when cleaning up annotation of the Node: %v, stderr: %s", err, stderr)
	}
	return nil
}

func initializeGateway(t *testing.T, data *MCTestData) {
	gatewayNames = make(map[string]string)
	regularNodeNames = make(map[string][]string)
	// Annotates a Node as Gateway, then member controller will create a Gateway correspondingly.
	for clusterName := range data.clusterTestDataMap {
		if clusterName == leaderCluster {
			// Skip Gateway initilization for the leader cluster
			continue
		}
		name, nodes, err := getNodeNameAndList(clusterName)
		failOnError(err, t)
		err = data.annotateGatewayNode(clusterName, name)
		failOnError(err, t)
		gatewayNames[clusterName] = name
		regularNodeNames[clusterName] = nodes
	}
}

func teardownGateway(t *testing.T, data *MCTestData) {
	for clusterName := range data.clusterTestDataMap {
		if clusterName == leaderCluster {
			continue
		}
		if _, ok := gatewayNames[clusterName]; ok {
			log.Infof("Removing the Gateway annotation on Node %s in cluster %s", gatewayNames[clusterName], clusterName)
			if err := data.deleteAnnotation(clusterName, gatewayNames[clusterName]); err != nil {
				log.Errorf("Error: %v", err)
			}
		}
	}
}
