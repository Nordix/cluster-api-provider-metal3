package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"k8s.io/utils/pointer"

	dockerTypes "github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/cluster-api/test/e2e/internal/log"
	framework "sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Doing Pivoting", func() {
	var (
		ctx                 = context.TODO()
		specName            = "metal3"
		namespace           = "metal3"
		cluster             *clusterv1.Cluster
		clusterName         = "test1"
		clusterctlLogFolder string
	)

	BeforeEach(func() {
		Expect(e2eConfig).ToNot(BeNil(), "Invalid argument. e2eConfig can't be nil when calling %s spec", specName)
		Expect(clusterctlConfigPath).To(BeAnExistingFile(), "Invalid argument. clusterctlConfigPath must be an existing file when calling %s spec", specName)
		Expect(bootstrapClusterProxy).ToNot(BeNil(), "Invalid argument. bootstrapClusterProxy can't be nil when calling %s spec", specName)
		Expect(os.MkdirAll(artifactFolder, 0755)).To(Succeed(), "Invalid argument. artifactFolder can't be created for %s spec", specName)

		Expect(e2eConfig.Variables).To(HaveKey(KubernetesVersion))

		// We need to override clusterctl apply log folder to avoid getting our credentials exposed.
		clusterctlLogFolder = filepath.Join(os.TempDir(), "clusters", bootstrapClusterProxy.GetName())
	})

	AfterEach(func() {
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cluster, e2eConfig.GetIntervals, clusterName, clusterctlLogFolder, skipCleanup)
	})

	Context("Creating a highly available control-plane cluster", func() {
		It("Should create a cluster with 1 control-plane and 1 worker nodes", func() {
			By("Creating a high available cluster")
			result := clusterctl.ApplyClusterTemplateAndWait(ctx, clusterctl.ApplyClusterTemplateAndWaitInput{
				ClusterProxy: bootstrapClusterProxy,
				ConfigCluster: clusterctl.ConfigClusterInput{
					LogFolder:                clusterctlLogFolder,
					ClusterctlConfigPath:     clusterctlConfigPath,
					KubeconfigPath:           bootstrapClusterProxy.GetKubeconfigPath(),
					InfrastructureProvider:   clusterctl.DefaultInfrastructureProvider,
					Flavor:                   "ha",
					Namespace:                namespace,
					ClusterName:              clusterName,
					KubernetesVersion:        e2eConfig.GetVariable(KubernetesVersion),
					ControlPlaneMachineCount: pointer.Int64Ptr(1),
					WorkerMachineCount:       pointer.Int64Ptr(1),
				},
				CNIManifestPath:              e2eTestsPath + "/data/cni/calico/calico.yaml",
				WaitForClusterIntervals:      e2eConfig.GetIntervals(specName, "wait-cluster"),
				WaitForControlPlaneIntervals: e2eConfig.GetIntervals(specName, "wait-control-plane"),
				WaitForMachineDeployments:    e2eConfig.GetIntervals(specName, "wait-worker-nodes"),
			})
			cluster = result.Cluster
		})

		It("Should successfully do pivoting ", func() {
			By("Remove Ironic containers in the source cluster")
			ironicContainerList := []string{
				"ironic-api",
				"ironic-conductor",
				"ironic-inspector",
				"dnsmasq",
				"httpd-reverse-proxy",
				"mariadb",
				"ironic-endpoint-keepalived",
				"ironic-log-watch",
				"ironic-inspector-log-watch",
			}
			// run docker command to delete
			//TODO: Delete the link below
			// https://gist.github.com/frikky/e2efcea6c733ea8d8d015b7fe8a91bf6
			dockerClient, err := docker.NewEnvClient()
			Expect(err).To(BeNil(), "Unable to get docker client")
			removeOptions := dockerTypes.ContainerRemoveOptions{
				Force: true,
			}
			for _, container := range ironicContainerList {
				err = dockerClient.ContainerRemove(ctx, container, removeOptions)
				Expect(err).NotTo(BeNil(), "Unable to delete the container %s", container)
			}

			By("Create Ironic namespace")
			//bootstrapClusterProxy
			targetCluster := bootstrapClusterProxy.GetWorkloadCluster(ctx, clusterName, namespace)
			targetClusterClient := targetCluster.GetClientSet()
			// get namespace from env. Need to check
			ironicNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: e2eConfig.GetVariable(IronicNamespace),
				},
			}

			_, err = targetClusterClient.CoreV1().Namespaces().Create(ironicNamespace)
			Expect(err).To(BeNil(), "Unable to create the Ironic namespace")

			By("Configure Ironic Configmap")
			configureIronicConfigmap(true)

			By("Install Ironic in the target cluster")
			installIronic(targetCluster)

			By("Reinstate Ironic Configmap")
			configureIronicConfigmap(false)

			By("Initialize Provider component in target cluster")
			clusterctl.InitManagementClusterAndWatchControllerLogs(ctx, clusterctl.InitManagementClusterAndWatchControllerLogsInput{
				ClusterProxy:            targetCluster,
				ClusterctlConfigPath:    clusterctlConfigPath,
				InfrastructureProviders: e2eConfig.InfrastructureProviders(),
				LogFolder:               filepath.Join(artifactFolder, "clusters", clusterName+"-pivoting"),
			}, e2eConfig.GetIntervals(specName, "wait-controllers"))

			By("Ensure API servers are stable before doing move")
			// Nb. This check was introduced to prevent doing move to self-hosted in an aggressive way and thus avoid flakes.
			// More specifically, we were observing the test failing to get objects from the API server during move, so we
			// are now testing the API servers are stable before starting move.
			Consistently(func() error {
				kubeSystem := &corev1.Namespace{}
				return bootstrapClusterProxy.GetClient().Get(ctx, client.ObjectKey{Name: "kube-system"}, kubeSystem)
			}, "5s", "100ms").Should(BeNil(), "Failed to assert bootstrap API server stability")
			Consistently(func() error {
				kubeSystem := &corev1.Namespace{}
				return targetCluster.GetClient().Get(ctx, client.ObjectKey{Name: "kube-system"}, kubeSystem)
			}, "5s", "100ms").Should(BeNil(), "Failed to assert target API server stability")

			By("Moving the cluster to self hosted")
			clusterctl.Move(ctx, clusterctl.MoveInput{
				LogFolder:            filepath.Join(artifactFolder, "clusters", clusterName+"-bootstrap"),
				ClusterctlConfigPath: clusterctlConfigPath,
				FromKubeconfigPath:   bootstrapClusterProxy.GetKubeconfigPath(),
				ToKubeconfigPath:     targetCluster.GetKubeconfigPath(),
				Namespace:            namespace,
			})

			log.Logf("Waiting for the cluster to be reconciled after moving to self hosted")
			pivotingCluster := framework.DiscoveryAndWaitForCluster(ctx, framework.DiscoveryAndWaitForClusterInput{
				Getter:    targetCluster.GetClient(),
				Namespace: namespace,
				Name:      cluster.Name,
			}, e2eConfig.GetIntervals(specName, "wait-cluster")...)

			controlPlane := framework.GetKubeadmControlPlaneByCluster(ctx, framework.GetKubeadmControlPlaneByClusterInput{
				Lister:      targetCluster.GetClient(),
				ClusterName: pivotingCluster.Name,
				Namespace:   pivotingCluster.Namespace,
			})
			Expect(controlPlane).ToNot(BeNil())

			By("PASSED!")

		})
	})
})

func configureIronicConfigmap(isIronicDeployed bool) {
	ironicConfigmap := fmt.Sprint("%s/ironic-deployment/keepalived/ironic_bmo_configmap.env", e2eConfig.GetVariable(BmoPath))
	newIronicConfigmap := fmt.Sprint("%s/ironic_bmo_configmap.env", e2eConfig.GetVariable(IronicDataDir))
	backupIronicConfigmap := fmt.Sprint("%s/ironic-deployment/keepalived/ironic_bmo_configmap.env.orig", e2eConfig.GetVariable(BmoPath))
	if isIronicDeployed {
		cmd := exec.Command("cp", ironicConfigmap, backupIronicConfigmap)
		err := cmd.Run()
		Expect(err).To(BeNil(), "Cannot run cp command")
		cmd = exec.Command("cp", newIronicConfigmap, ironicConfigmap)
		err = cmd.Run()
		Expect(err).To(BeNil(), "Cannot run cp command")
	} else {
		cmd := exec.Command("mv", backupIronicConfigmap, ironicConfigmap)
		err := cmd.Run()
		Expect(err).To(BeNil(), "Cannot run mv command")
	}
}

func installIronic(targetCluster framework.ClusterProxy) {
	// TODO: Delete the comments
	// - name: Install Ironic
	//     shell: "{{ BMOPATH }}/tools/deploy.sh false true {{ IRONIC_TLS_SETUP }} {{ IRONIC_BASIC_AUTH }} true"
	//     environment:
	//       IRONIC_HOST: "{{ IRONIC_HOST }}"
	//       IRONIC_HOST_IP: "{{ IRONIC_HOST_IP }}"
	//       KUBECTL_ARGS: "{{ KUBECTL_ARGS }}"
	path := fmt.Sprint("%s/tools/", e2eConfig.GetVariable(BmoPath))
	args := []string{
		"deploy.sh",
		"false",
		"true",
		e2eConfig.GetVariable(IronicTLSEnable),
		e2eConfig.GetVariable(IronicBasicAuth),
		"true",
	}
	env := []string{
		fmt.Sprint("IRONIC_HOST=%s", e2eConfig.GetVariable(IronicHost)),
		fmt.Sprint("IRONIC_HOST_IP=%s", e2eConfig.GetVariable(IronicHost)),
		fmt.Sprint("KUBECTL_ARGS='--kubeconfig=%s'", targetCluster.GetKubeconfigPath()),
	}
	cmd := exec.Cmd{
		Path: path,
		Args: args,
		Env:  env,
	}
	err := cmd.Run()
	Expect(err).To(BeNil(), "Fail to deploy Ironic")
}
