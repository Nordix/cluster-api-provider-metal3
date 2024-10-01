package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	infrav1 "github.com/metal3-io/cluster-api-provider-metal3/api/v1beta1"
	"net/http"
	b64 "encoding/base64"
	"io/ioutil"
	"encoding/json"
)

var (
	ctx                      = context.TODO()
	specName                 = "metal3"
	namespace                = "metal3"
	clusterName              = "test1"
	clusterctlLogFolder      string
	targetCluster            framework.ClusterProxy
	controlPlaneMachineCount int64
	workerMachineCount       int64
)

/*
 * Pivoting-based feature tests
 * This test evaluates capm3 feature in a pivoted cluster, which means migrating resources and control components from
 * bootsrap to target cluster
 *
 * Tested Features:
 * - Create a workload cluster
 * - Pivot to self-hosted
 * - Test Certificate Rotation
 * - Test Node Reuse
 *
 * Pivot to self-hosted:
 * - the Ironic containers removed from the source cluster, and a new Ironic namespace is created in the target cluster.
 * - The provider components are initialized in the target cluster using `clusterctl.Init`.
 * - Ironic is installed in the target cluster, followed by the installation of BMO.
 * - The stability of the API servers is checked before proceeding with the move to self-hosted.
 * - The cluster is moved to self-hosted using `clusterctl.Move`.
 * - After the move, various checks are performed to ensure that the cluster resources are in the expected state.
 * - If all checks pass, the test is considered successful.
 *
 * Certificate Rotation:
 * This test ensures that certificate rotation in the Ironic pod is functioning correctly.
 * by forcing certificate regeneration and verifying container restarts
 * - It starts by checking if the Ironic pod is running. It retrieves the Ironic deployment and waits for the pod to be in the "Running" state.
 * - The test forces cert-manager to regenerate the certificates by deleting the relevant secrets.
 * - It then waits for the containers in the Ironic pod to be restarted. It checks if each container exists and compares the restart count with the previously recorded values.
 * - If all containers are restarted successfully, the test passes.
 *
 * Node Reuse:
 * This test verifies the feature of reusing the same node after upgrading Kubernetes version in KubeadmControlPlane (KCP) and MachineDeployment (MD) nodes.
 * Note that while other controlplane providers are expected to work only KubeadmControlPlane is currently tested.
 * - The test starts with a cluster containing 3 KCP (Kubernetes control plane) nodes and 1 MD (MachineDeployment) node.
 * - The control plane nodes are untainted to allow scheduling new pods on them.
 * - The MachineDeployment is scaled down to 0 replicas, ensuring that all worker nodes will be deprovisioned. This provides 1 BMH (BareMetalHost) available for reuse during the upgrade.
 * - The code waits for one BareMetalHost (BMH) to become available, indicating that one worker node is deprovisioned and available for reuse.
 * - The names and UUIDs of the provisioned BMHs before the upgrade are obtained.
 * - An image is downloaded, and the nodeReuse field is set to True for the existing KCP Metal3MachineTemplate to reuse the node.
 * - A new Metal3MachineTemplate with the upgraded image is created for the KCP.
 * - The KCP is updated to upgrade the Kubernetes version and binaries. The rolling update strategy is set to update one machine at a time (MaxSurge: 0).
 * - The code waits for one machine to enter the deleting state and ensures that no new machines are in the provisioning state.
 * - The code waits for the deprovisioning BMH to become available again.
 * - It checks if the deprovisioned BMH is reused for the next provisioning.
 * - The code waits for the second machine to become running and updated with the new Kubernetes version.
 * - The upgraded control plane nodes are untainted to allow scheduling worker pods on them.
 * - Check all control plane nodes become running and update with the new Kubernetes version.
 * - The names and UUIDs of the provisioned BMHs after the upgrade are obtained.
 * - The difference between the mappings before and after the upgrade is checked to ensure that the same BMHs were reused.
 * - Similar steps are performed to test machine deployment node reuse.
 *
 * Finally, the cluster is re-pivoted and cleaned up.
 */

var _ = Describe("Testing features in ephemeral or target cluster [pivoting] [features]", Label("pivoting", "features"),
	func() {

		BeforeEach(func() {
			osType := strings.ToLower(os.Getenv("OS"))
			Expect(osType).ToNot(Equal(""))
			validateGlobals(specName)

			// We need to override clusterctl apply log folder to avoid getting our credentials exposed.
			clusterctlLogFolder = filepath.Join(os.TempDir(), "clusters", bootstrapClusterProxy.GetName())
		})

		It("Should get a management cluster then test cert rotation and node reuse", func() {
			targetCluster, _ = createTargetCluster(e2eConfig.GetVariable("FROM_K8S_VERSION"))
			managementCluster := bootstrapClusterProxy
			// If not running ephemeral test, use the target cluster for management
			if !ephemeralTest {
				managementCluster = targetCluster
				pivoting(ctx, func() PivotingInput {
					return PivotingInput{
						E2EConfig:             e2eConfig,
						BootstrapClusterProxy: bootstrapClusterProxy,
						TargetCluster:         targetCluster,
						SpecName:              specName,
						ClusterName:           clusterName,
						Namespace:             namespace,
						ArtifactFolder:        artifactFolder,
						ClusterctlConfigPath:  clusterctlConfigPath,
					}
				})
			}

			certRotation(ctx, func() CertRotationInput {
				return CertRotationInput{
					E2EConfig:         e2eConfig,
					ManagementCluster: managementCluster,
					SpecName:          specName,
				}
			})

			nodeReuse(ctx, func() NodeReuseInput {
				return NodeReuseInput{
					E2EConfig:         e2eConfig,
					ManagementCluster: managementCluster,
					TargetCluster:     targetCluster,
					SpecName:          specName,
					ClusterName:       clusterName,
					Namespace:         namespace,
				}
			})
		})

		AfterEach(func() {
			Logf("Logging state of bootstrap cluster")
			ListBareMetalHosts(ctx, bootstrapClusterProxy.GetClient(), client.InNamespace(namespace))
			ListMetal3Machines(ctx, bootstrapClusterProxy.GetClient(), client.InNamespace(namespace))
			ListMachines(ctx, bootstrapClusterProxy.GetClient(), client.InNamespace(namespace))
			ListNodes(ctx, bootstrapClusterProxy.GetClient())
			Logf("Logging state of target cluster")
			if !ephemeralTest {
				ListBareMetalHosts(ctx, targetCluster.GetClient(), client.InNamespace(namespace))
				ListMetal3Machines(ctx, targetCluster.GetClient(), client.InNamespace(namespace))
				ListMachines(ctx, targetCluster.GetClient(), client.InNamespace(namespace))
			}
			ListNodes(ctx, targetCluster.GetClient())
			if !ephemeralTest {
				// Dump the target cluster resources before re-pivoting.
				Logf("Dump the target cluster resources before re-pivoting")
				framework.DumpAllResources(ctx, framework.DumpAllResourcesInput{
					Lister:    targetCluster.GetClient(),
					Namespace: namespace,
					LogPath:   filepath.Join(artifactFolder, "clusters", clusterName, "resources"),
				})

				rePivoting(ctx, func() RePivotingInput {
					return RePivotingInput{
						E2EConfig:             e2eConfig,
						BootstrapClusterProxy: bootstrapClusterProxy,
						TargetCluster:         targetCluster,
						SpecName:              specName,
						ClusterName:           clusterName,
						Namespace:             namespace,
						ArtifactFolder:        artifactFolder,
						ClusterctlConfigPath:  clusterctlConfigPath,
					}
				})
			}
			DumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, e2eConfig.GetIntervals, clusterName, clusterctlLogFolder, skipCleanup)
		})

	})

type Endpoint struct {
	Host string
	Port int
}
func check(e error){
	if e != nil {
		panic(e)
	}
}
func createFakeTargetCluster(k8sVersion string) (framework.ClusterProxy, *clusterctl.ApplyClusterTemplateAndWaitResult) {

	caKey, err:= os.ReadFile("/tmp/ca.key")
	check(err)
	caCert, err:= os.ReadFile("/tmp/ca.crt")
	check(err)
	etcdKey, err:= os.ReadFile("/tmp/etcd.key")
	check(err)
	etcdCert, err:= os.ReadFile("/tmp/etcd.crt")
	check(err)
	caKeyEncoded:=b64.StdEncoding.EncodeToString(caKey)
	caCertEncoded:=b64.StdEncoding.EncodeToString(caCert)
	etcdKeyEncoded:=b64.StdEncoding.EncodeToString(etcdKey)
	etcdCertEncoded:=b64.StdEncoding.EncodeToString(etcdCert)
	os.Setenv("CA_KEY_ENCODED", caKeyEncoded)
	os.Setenv("CA_CERT_ENCODED", caCertEncoded)
	os.Setenv("ETCD_KEY_ENCODED", etcdKeyEncoded)
	os.Setenv("ETCD_CERT_ENCODED", etcdCertEncoded)
	cluster_endpoints, err :=http.Get("http://172.22.0.2:3333/register?resource=metal3/test1&caKey="+caKeyEncoded+"&caCert="+caCertEncoded+"&etcdKey="+etcdKeyEncoded+"&etcdCert="+etcdCertEncoded)
	check(err)
	defer cluster_endpoints.Body.Close()
	body, err := ioutil.ReadAll(cluster_endpoints.Body)
	check(err)
	var response Endpoint
	json.Unmarshal(body, &response)
	Logf("CLUSTER_APIENDPOINT_HOST %v CLUSTER_APIENDPOINT_PORT %v", response.Host, response.Port)
	os.Setenv("CLUSTER_APIENDPOINT_HOST", response.Host)
	os.Setenv("CLUSTER_APIENDPOINT_PORT", fmt.Sprintf("%v",response.Port))
	return createTargetCluster(k8sVersion)

}
func createTargetCluster(k8sVersion string) (framework.ClusterProxy, *clusterctl.ApplyClusterTemplateAndWaitResult) {
	By("Creating a high available cluster")
	imageURL, imageChecksum := EnsureImage(k8sVersion)
	os.Setenv("IMAGE_RAW_CHECKSUM", imageChecksum)
	os.Setenv("IMAGE_RAW_URL", imageURL)
	controlPlaneMachineCount = int64(numberOfControlplane)
	workerMachineCount = int64(numberOfWorkers)
	result := clusterctl.ApplyClusterTemplateAndWaitResult{}
	clusterctl.ApplyClusterTemplateAndWait(ctx, clusterctl.ApplyClusterTemplateAndWaitInput{
		ClusterProxy: bootstrapClusterProxy,
		ConfigCluster: clusterctl.ConfigClusterInput{
			LogFolder:                clusterctlLogFolder,
			ClusterctlConfigPath:     clusterctlConfigPath,
			KubeconfigPath:           bootstrapClusterProxy.GetKubeconfigPath(),
			InfrastructureProvider:   clusterctl.DefaultInfrastructureProvider,
			Flavor:                   osType,
			Namespace:                namespace,
			ClusterName:              clusterName,
			KubernetesVersion:        k8sVersion,
			ControlPlaneMachineCount: &controlPlaneMachineCount,
			WorkerMachineCount:       &workerMachineCount,
		},
		PreWaitForCluster: func () {
			// get bmh
			// get m3m
			// waiting machine
			By("Waiting for one Machine to be provisioning")
			WaitForNumMachinesInState(ctx, clusterv1.MachinePhaseProvisioning, WaitForNumInput{
				Client:    bootstrapClusterProxy.GetClient(),
				Options:   []client.ListOption{client.InNamespace(namespace)},
				Replicas:  1,
				Intervals:  e2eConfig.GetIntervals(specName, "wait-machine-remediation"),
			})


			metal3Machines := infrav1.Metal3MachineList{}
			metal3Machines_updated :=  ""
			bootstrapClusterProxy.GetClient().List(ctx, &metal3Machines, []client.ListOption{client.InNamespace(namespace)}...)
			for _, m3machine := range metal3Machines.Items {
				if (m3machine.GetAnnotations()["metal3.io/BareMetalHost"] != ""){
					providerID:="metal3://metal3/"+Metal3MachineToBmhName(m3machine)+"/"+m3machine.GetName()
					machine, _ := Metal3MachineToMachineName(m3machine)
					Logf("http://172.22.0.2:3333/updateNode?resource=metal3/test1&nodeName="+machine+"&providerID="+providerID)
					resp, err :=http.Get("http://172.22.0.2:3333/updateNode?resource=metal3/test1&nodeName="+machine+"&providerID="+providerID)
					metal3Machines_updated= m3machine.GetName()
				Logf("resp : %v err: %v", resp, err)
				}
				
			}
			By("Waiting for the other Machine to be provisioning")
			WaitForNumMachinesInState(ctx, clusterv1.MachinePhaseProvisioning, WaitForNumInput{
				Client:    bootstrapClusterProxy.GetClient(),
				Options:   []client.ListOption{client.InNamespace(namespace)},
				Replicas:  2,
				Intervals:  e2eConfig.GetIntervals(specName, "wait-machine-remediation"),
			})
			for _, m3machine := range metal3Machines.Items {
				if (m3machine.GetName() != metal3Machines_updated){
					providerID:="metal3://metal3/"+Metal3MachineToBmhName(m3machine)+"/"+m3machine.GetName()
					machine, _ := Metal3MachineToMachineName(m3machine)
					Logf("http://172.22.0.2:3333/updateNode?resource=metal3/test1&nodeName="+machine+"&providerID="+providerID)
					resp, err :=http.Get("http://172.22.0.2:3333/updateNode?resource=metal3/test1&nodeName="+machine+"&providerID="+providerID)
				Logf("resp : %v err: %v", resp, err)
				}
				
			}
		},
		WaitForClusterIntervals:      e2eConfig.GetIntervals(specName, "wait-cluster"),
		WaitForControlPlaneIntervals: e2eConfig.GetIntervals(specName, "wait-control-plane"),
		WaitForMachineDeployments:    e2eConfig.GetIntervals(specName, "wait-worker-nodes"),
	}, &result)
	targetCluster := bootstrapClusterProxy.GetWorkloadCluster(ctx, namespace, clusterName)
	framework.WaitForPodListCondition(ctx, framework.WaitForPodListConditionInput{
		Lister:      targetCluster.GetClient(),
		ListOptions: &client.ListOptions{LabelSelector: labels.Everything(), Namespace: "kube-system"},
		Condition:   framework.PhasePodCondition(corev1.PodRunning),
	}, e2eConfig.GetIntervals(specName, "wait-all-pod-to-be-running-on-target-cluster")...)
	return targetCluster, &result
}
