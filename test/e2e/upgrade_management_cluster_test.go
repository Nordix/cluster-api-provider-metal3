package e2e

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	bmo "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	clusterv1old "sigs.k8s.io/cluster-api/api/v1alpha3"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client/config"
	capi_e2e "sigs.k8s.io/cluster-api/test/e2e"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func upgradeManagementCluster() {
	var (
		ctx                 = context.TODO()
		specName            = "metal3"
		namespace           = "metal3"
		clusterName         = "test1"
		workloadClusterName = "test2"
		clusterctlLogFolder string
		kubernetesVersion   = e2eConfig.GetVariable(KubernetesVersion)
	)
	/*---------------------------------------------*
	| Create a target cluster with v1a4 clusterctl |
	*----------------------------------------------*/
	Logf("Starting v1a5 to v1b1 upgrade tests")

	clusterctlBinaryURLTemplate := e2eConfig.GetVariable("INIT_WITH_BINARY")
	clusterctlBinaryURLReplacer := strings.NewReplacer("{OS}", runtime.GOOS, "{ARCH}", runtime.GOARCH)
	clusterctlBinaryURL := clusterctlBinaryURLReplacer.Replace(clusterctlBinaryURLTemplate)

	Logf("Downloading clusterctl binary from %s", clusterctlBinaryURL)
	clusterctlBinaryPath := downloadToTmpFile(clusterctlBinaryURL)
	defer os.Remove(clusterctlBinaryPath) // clean up

	err := os.Chmod(clusterctlBinaryPath, 0744) //nolint:gosec
	Expect(err).ToNot(HaveOccurred(), "failed to chmod temporary file")

	By("Creating a high available cluster")
	Logf("Getting the cluster template yaml")
	upgradeClusterTemplate := clusterctl.ConfigClusterWithBinary(ctx, clusterctlBinaryPath, clusterctl.ConfigClusterInput{
		KubeconfigPath:       bootstrapClusterProxy.GetKubeconfigPath(),
		ClusterctlConfigPath: clusterctlConfigPath,
		// define template variables
		Namespace:                namespace,
		ClusterName:              clusterName,
		KubernetesVersion:        kubernetesVersion,
		ControlPlaneMachineCount: pointer.Int64Ptr(3),
		WorkerMachineCount:       pointer.Int64Ptr(1),
		InfrastructureProvider:   clusterctl.DefaultInfrastructureProvider,
		// setup clusterctl logs folder
		LogFolder: clusterctlLogFolder,
	})

	Expect(upgradeClusterTemplate).ToNot(BeNil(), "Failed to get the cluster template")

	Logf("Applying the cluster template yaml to the cluster:")
	Logf(string(upgradeClusterTemplate))

	Expect(bootstrapClusterProxy.Apply(ctx, upgradeClusterTemplate)).To(Succeed())

	By("Waiting for the machines to be running")
	Eventually(func() (int, error) {
		n := 0
		machineList := &clusterv1old.MachineList{}
		if err := bootstrapClusterProxy.GetClient().List(ctx, machineList, client.InNamespace(namespace), client.MatchingLabels{clusterv1.ClusterLabelName: clusterName}); err == nil {
			for _, machine := range machineList.Items {
				if strings.EqualFold(machine.Status.Phase, "running") {
					n++
				}
			}
		}
		return n, nil
	}, e2eConfig.GetIntervals(specName, "wait-worker-nodes")...).Should(Equal(4))

	upgradeClusterProxy := bootstrapClusterProxy.GetWorkloadCluster(ctx, namespace, clusterName)
	upgradeClusterClient := upgradeClusterProxy.GetClient()

	// Apply CNI
	cniYaml, err := os.ReadFile(e2eConfig.GetVariable(capi_e2e.CNIPath))
	Expect(err).ShouldNot(HaveOccurred())
	Expect(upgradeClusterProxy.Apply(ctx, cniYaml)).ShouldNot(HaveOccurred())
	By("Initializing the workload cluster with older versions of providers")

	contract := e2eConfig.GetVariable("CAPI_VERSION")

	clusterctl.InitManagementClusterAndWatchControllerLogs(ctx, clusterctl.InitManagementClusterAndWatchControllerLogsInput{
		ClusterctlBinaryPath:    clusterctlBinaryPath, // use older version of clusterctl to init the management cluster
		ClusterProxy:            upgradeClusterProxy,
		ClusterctlConfigPath:    clusterctlConfigPath,
		CoreProvider:            e2eConfig.GetProviderLatestVersionsByContract(contract, config.ClusterAPIProviderName)[0],
		BootstrapProviders:      e2eConfig.GetProviderLatestVersionsByContract(contract, config.KubeadmBootstrapProviderName),
		ControlPlaneProviders:   e2eConfig.GetProviderLatestVersionsByContract(contract, config.KubeadmControlPlaneProviderName),
		InfrastructureProviders: e2eConfig.GetProviderLatestVersionsByContract(contract, e2eConfig.InfrastructureProviders()...),
		LogFolder:               filepath.Join(artifactFolder, "clusters", clusterName),
	}, e2eConfig.GetIntervals(specName, "wait-controllers")...)

	/*-------------------------------------------------*
	| Pivote to run ironic/BMO and resources on target |
	*--------------------------------------------------*/
	By("Start pivoting")
	By("Remove Ironic containers from the source cluster")
	ephemeralCluster := os.Getenv("EPHEMERAL_CLUSTER")
	if ephemeralCluster == "kind" {
		removeIronicContainers()
	} else {
		removeIronicDeployment()
	}

	By("Create Ironic namespace")
	upgradeClusterClientSet := upgradeClusterProxy.GetClientSet()
	ironicNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: os.Getenv("IRONIC_NAMESPACE"),
		},
	}
	_, err = upgradeClusterClientSet.CoreV1().Namespaces().Create(ctx, ironicNamespace, metav1.CreateOptions{})
	Expect(err).To(BeNil(), "Unable to create the Ironic namespace")

	By("Configure Ironic Configmap")
	configureIronicConfigmap(true)

	By("Add labels to BMO CRDs")
	labelBMOCRDs(nil)

	By("Install BMO")
	installIronicBMO(upgradeClusterProxy, "false", "true")

	By("Install Ironic in the target cluster")
	installIronicBMO(upgradeClusterProxy, "true", "false")

	By("Restore original BMO configmap")
	restoreBMOConfigmap()
	By("Reinstate Ironic Configmap")
	configureIronicConfigmap(false)

	By("Add labels to BMO CRDs in the target cluster")
	labelBMOCRDs(upgradeClusterProxy)

	By("Ensure API servers are stable before doing move")
	Consistently(func() error {
		kubeSystem := &corev1.Namespace{}
		return bootstrapClusterProxy.GetClient().Get(ctx, client.ObjectKey{Name: "kube-system"}, kubeSystem)
	}, "5s", "100ms").Should(BeNil(), "Failed to assert bootstrap API server stability")
	Consistently(func() error {
		kubeSystem := &corev1.Namespace{}
		return upgradeClusterClient.Get(ctx, client.ObjectKey{Name: "kube-system"}, kubeSystem)
	}, "5s", "100ms").Should(BeNil(), "Failed to assert target API server stability")
	By("Moving the cluster to self hosted")

	cmd := exec.Command(clusterctlBinaryPath, "move", "--to-kubeconfig", upgradeClusterProxy.GetKubeconfigPath(), "-n", namespace, "-v", "10")
	output, err := cmd.CombinedOutput()
	Logf("move: %v", string(output))
	Expect(err).To(BeNil())

	By("Check if BMH is in provisioned state")
	Eventually(func() error {
		bmhList := &bmo.BareMetalHostList{}
		if err := upgradeClusterClient.List(ctx, bmhList, client.InNamespace(namespace)); err != nil {
			return err
		}
		for _, bmh := range bmhList.Items {
			if !bmh.WasProvisioned() {
				return errors.New("BMHs cannot be provisioned")
			}
		}
		return nil
	}, e2eConfig.GetIntervals(specName, "wait-object-provisioned")...).Should(BeNil())

	Logf("Scale down KCP to 1 in order to 2 VMs for the workload cluster")
	cmd = exec.Command("kubectl", "scale", "kcp", clusterName, "-n", namespace, "--replicas", "1", "--kubeconfig", upgradeClusterProxy.GetKubeconfigPath())
	output, err = cmd.CombinedOutput()
	Logf("scale: %v", string(output))
	Expect(err).To(BeNil())

	Logf("Waiting for 2 BMHs to be in Available state")
	Eventually(func(g Gomega) {
		bmhs, err := getAllBmhs(ctx, upgradeClusterClient, namespace, specName)
		g.Expect(err).To(BeNil())
		g.Expect(filterBmhsByProvisioningState(bmhs, bmo.StateAvailable)).To(HaveLen(2))
	}, e2eConfig.GetIntervals(specName, "wait-machine-remediation")...).Should(Succeed())

	/*-------------------------------*
	| Create a test workload cluster |
	*--------------------------------*/
	var (
		controlPlaneMachineCount = pointer.Int64Ptr(1)
		workerMachineCount       = pointer.Int64Ptr(1)
	)
	By("Creating a test workload cluster")

	Logf("Creating the workload cluster with name %q using the %q template (Kubernetes %s, %d control-plane machines, %d worker machines)",
		workloadClusterName, "(default)", kubernetesVersion, *controlPlaneMachineCount, *workerMachineCount)

	Logf("Getting the cluster template yaml")
	Logf("clusterctlBinaryPath = %v", clusterctlBinaryPath)
	Logf("ClusterctlConfigPath = %v", clusterctlConfigPath)
	workloadClusterTemplate := clusterctl.ConfigClusterWithBinary(ctx, clusterctlBinaryPath, clusterctl.ConfigClusterInput{
		// pass reference to the management cluster hosting this test
		KubeconfigPath: upgradeClusterProxy.GetKubeconfigPath(),
		// pass the clusterctl config file that points to the local provider repository created for this test,

		ClusterctlConfigPath: clusterctlConfigPath,

		// define template variables
		Namespace:                namespace,
		ClusterName:              workloadClusterName,
		Flavor:                   "ha-upgrade-workload",
		KubernetesVersion:        kubernetesVersion,
		ControlPlaneMachineCount: controlPlaneMachineCount,
		WorkerMachineCount:       workerMachineCount,
		InfrastructureProvider:   clusterctl.DefaultInfrastructureProvider,
		// setup clusterctl logs folder
		LogFolder: filepath.Join(artifactFolder, "clusters", upgradeClusterProxy.GetName()),
	})
	Expect(workloadClusterTemplate).ToNot(BeNil(), "Failed to get the cluster template")
	Logf("%v ", string(workloadClusterTemplate))

	Logf("Applying the cluster template yaml to the cluster")
	Expect(upgradeClusterProxy.Apply(ctx, workloadClusterTemplate)).To(Succeed())

	By("Waiting for the machines to be running")
	Eventually(func() (int64, error) {
		var n int64
		machineList := &clusterv1old.MachineList{}
		if err := upgradeClusterClient.List(ctx, machineList, client.InNamespace(namespace), client.MatchingLabels{clusterv1.ClusterLabelName: workloadClusterName}); err == nil {
			for _, machine := range machineList.Items {
				if strings.EqualFold(machine.Status.Phase, "running") {
					n++
				}
			}
		}
		return n, nil
	}, e2eConfig.GetIntervals(specName, "wait-worker-nodes")...).Should(Equal(*controlPlaneMachineCount))

	By("THE MANAGEMENT CLUSTER WITH OLDER VERSION OF PROVIDERS WORKS!")

	/*-------------------------------*
	| Upgrade the management cluster |
	*--------------------------------*/
	By("Get the management cluster images before upgrading")
	pods, err := upgradeClusterClientSet.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	Expect(err).To(BeNil())
	printImages(pods)

	By("Upgrading providers to the latest version available")
	pwd, err := os.Getwd()
	Expect(err).To(BeNil())
	Logf("PWD:", pwd)

	type vars struct {
		ARTIFACTS            string
		CAPM3_REL_TO_VERSION string
	}
	home, err := os.UserHomeDir()
	Expect(err).To(BeNil())
	templateVars := vars{artifactFolder, e2eConfig.GetVariable("CAPM3_REL_TO_VERSION")}

	clusterctlUpgradeTestTemplate := filepath.Join(pwd, "/../../templates/test/upgrade/clusterctl-upgrade-test.yaml")
	templates := template.Must(template.New("clusterctl-upgrade-test.yaml").ParseFiles(clusterctlUpgradeTestTemplate))
	// Create a file to store the template output
	clusterctlFile, err := os.Create(fmt.Sprintf("%s/.cluster-api/clusterctl.yaml", home))
	Expect(err).To(BeNil())
	// Execute the templates and write the output to the created file
	err = templates.Execute(clusterctlFile, templateVars)
	Expect(err).To(BeNil())
	clusterctlFile.Close()

	Logf("clusterv1.GroupVersion.Version: %v", clusterv1.GroupVersion.Version)
	clusterctl.UpgradeManagementClusterAndWait(ctx, clusterctl.UpgradeManagementClusterAndWaitInput{
		ClusterctlConfigPath: e2eConfig.GetVariable("CONFIG_FILE_PATH"),
		ClusterProxy:         upgradeClusterProxy,
		Contract:             clusterv1.GroupVersion.Version,
		LogFolder:            filepath.Join(artifactFolder, "clusters", workloadClusterName),
	}, e2eConfig.GetIntervals(specName, "wait-controllers")...)

	By("THE MANAGEMENT CLUSTER WAS SUCCESSFULLY UPGRADED!")

	By("Get the management cluster images after upgrading")
	pods, err = upgradeClusterClientSet.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	Expect(err).To(BeNil())
	printImages(pods)

	By("UPGRADE MANAGEMENT CLUSTER PASSED!")
}

func downloadToTmpFile(url string) string {
	tmpFile, err := ioutil.TempFile("", "clusterctl")
	Expect(err).ToNot(HaveOccurred(), "failed to get temporary file")
	defer tmpFile.Close()

	// Get the data
	resp, err := http.Get(url) //nolint:gosec
	Expect(err).ToNot(HaveOccurred(), "failed to get clusterctl")
	defer resp.Body.Close()

	// Write the body to file
	_, err = io.Copy(tmpFile, resp.Body)
	Expect(err).ToNot(HaveOccurred(), "failed to write temporary file")

	return tmpFile.Name()
}
func printImages(pods *corev1.PodList) {
	var images []string
	for _, pod := range pods.Items {
		for _, c := range pod.Spec.Containers {
			exist := false
			for _, i := range images {
				if i == c.Image {
					exist = true
					break
				}
			}
			if !exist {
				images = append(images, c.Image)
				Logf("%v", c.Image)
			}
		}
	}
}
