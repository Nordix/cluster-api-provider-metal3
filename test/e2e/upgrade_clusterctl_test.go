package e2e

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	bmov1alpha1 "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client/config"
	capi_e2e "sigs.k8s.io/cluster-api/test/e2e"
	framework "sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const workDir = "/opt/metal3-dev-env/"

var _ = Describe("When testing cluster upgrade from releases (v1.2=>current) [clusterctl-upgrade]", func() {
	BeforeEach(func() {
		osType := strings.ToLower(os.Getenv("OS"))
		Expect(osType).ToNot(Equal(""))
		validateGlobals(specName)
		imageURL, imageChecksum := EnsureImage("v1.23.17")
		os.Setenv("IMAGE_RAW_CHECKSUM", imageChecksum)
		os.Setenv("IMAGE_RAW_URL", imageURL)
		// We need to override clusterctl apply log folder to avoid getting our credentials exposed.
		clusterctlLogFolder = filepath.Join(os.TempDir(), "clusters", bootstrapClusterProxy.GetName())
	})
	capi_e2e.ClusterctlUpgradeSpec(ctx, func() capi_e2e.ClusterctlUpgradeSpecInput {
		return capi_e2e.ClusterctlUpgradeSpecInput{
			E2EConfig:                       e2eConfig,
			ClusterctlConfigPath:            clusterctlConfigPath,
			BootstrapClusterProxy:           bootstrapClusterProxy,
			ArtifactFolder:                  artifactFolder,
			SkipCleanup:                     skipCleanup,
			InitWithCoreProvider:            "cluster-api:v1.2.12",
			InitWithBootstrapProviders:      []string{"kubeadm:v1.2.12"},
			InitWithControlPlaneProviders:   []string{"kubeadm:v1.2.12"},
			InitWithInfrastructureProviders: []string{"metal3:v1.2.2"},
			InitWithKubernetesVersion:       "v1.23.17",
			WorkloadKubernetesVersion:       "v1.23.17",
			InitWithBinary:                  "https://github.com/kubernetes-sigs/cluster-api/releases/download/v1.2.12/clusterctl-{OS}-{ARCH}",
			PreInit: func(clusterProxy framework.ClusterProxy) {
				preInitFunc(clusterProxy)
				// Override capi/capm3 versions exported in preInit
				os.Setenv("CAPI_VERSION", "v1beta1")
				os.Setenv("CAPM3_VERSION", "v1beta1")
				os.Setenv("KUBECONFIG_BOOTSTRAP", bootstrapClusterProxy.GetKubeconfigPath())
			},
			PreWaitForCluster:           preWaitForCluster,
			PreUpgrade:                  preUpgrade,
			PreCleanupManagementCluster: preCleanupManagementCluster,
			MgmtFlavor:                  osType,
			WorkloadFlavor:              osType,
		}
	})
	AfterEach(func() {
		// Recreate bmh that was used in capi namespace in metal3
		//#nosec G204 -- We need to pass in the file name here.
		cmd := exec.Command("bash", "-c", "kubectl apply -f bmhosts_crs.yaml  -n metal3")
		cmd.Dir = workDir
		output, err := cmd.CombinedOutput()
		Logf("Applying bmh to metal3 namespace : \n %v", string(output))
		Expect(err).To(BeNil())
		// wait for all bmh to become available
		bootstrapClient := bootstrapClusterProxy.GetClient()
		ListBareMetalHosts(ctx, bootstrapClient, client.InNamespace(namespace))
		WaitForNumBmhInState(ctx, bmov1alpha1.StateAvailable, WaitForNumInput{
			Client:    bootstrapClient,
			Options:   []client.ListOption{client.InNamespace(namespace)},
			Replicas:  5,
			Intervals: e2eConfig.GetIntervals(specName, "wait-bmh-available"),
		})
		ListBareMetalHosts(ctx, bootstrapClient, client.InNamespace(namespace))
	})
})

var _ = Describe("When testing cluster upgrade from releases (v0.4=>current) [clusterctl-upgrade]", func() {
	BeforeEach(func() {
		osType := strings.ToLower(os.Getenv("OS"))
		Expect(osType).ToNot(Equal(""))
		validateGlobals(specName)
		imageURL, imageChecksum := EnsureImage("v1.23.17")
		os.Setenv("IMAGE_RAW_CHECKSUM", imageChecksum)
		os.Setenv("IMAGE_RAW_URL", imageURL)
		// We need to override clusterctl apply log folder to avoid getting our credentials exposed.
		clusterctlLogFolder = filepath.Join(os.TempDir(), "clusters", bootstrapClusterProxy.GetName())
	})
	capi_e2e.ClusterctlUpgradeSpec(ctx, func() capi_e2e.ClusterctlUpgradeSpecInput {
		return capi_e2e.ClusterctlUpgradeSpecInput{
			E2EConfig:                       e2eConfig,
			ClusterctlConfigPath:            clusterctlConfigPath,
			BootstrapClusterProxy:           bootstrapClusterProxy,
			ArtifactFolder:                  artifactFolder,
			SkipCleanup:                     skipCleanup,
			InitWithCoreProvider:            "cluster-api:v0.4.8",
			InitWithBootstrapProviders:      []string{"kubeadm:v0.4.8"},
			InitWithControlPlaneProviders:   []string{"kubeadm:v0.4.8"},
			InitWithInfrastructureProviders: []string{"metal3:v0.5.5"},
			InitWithKubernetesVersion:       "v1.23.17",
			WorkloadKubernetesVersion:       "v1.23.17",
			InitWithBinary:                  "https://github.com/kubernetes-sigs/cluster-api/releases/download/v0.4.8/clusterctl-{OS}-{ARCH}",
			InitWithProvidersContract:       "v1alpha4",
			PreInit: func(clusterProxy framework.ClusterProxy) {
				preInitFunc(clusterProxy)
				// Override capi/capm3 versions exported in preInit
				os.Setenv("CAPI_VERSION", "v1alpha4")
				os.Setenv("CAPM3_VERSION", "v1alpha5")
				os.Setenv("KUBECONFIG_BOOTSTRAP", bootstrapClusterProxy.GetKubeconfigPath())
			},
			PreWaitForCluster:           preWaitForCluster,
			PreUpgrade:                  preUpgrade,
			PreCleanupManagementCluster: preCleanupManagementCluster,
			MgmtFlavor:                  osType,
			WorkloadFlavor:              osType,
		}
	})
	AfterEach(func() {
		// Recreate bmh that was used in capi namespace in metal3
		//#nosec G204 -- We need to pass in the file name here.
		cmd := exec.Command("bash", "-c", "kubectl apply -f bmhosts_crs.yaml  -n metal3")
		cmd.Dir = workDir
		output, err := cmd.CombinedOutput()
		Logf("Applying bmh to metal3 namespace : \n %v", string(output))
		Expect(err).To(BeNil())
		// wait for all bmh to become available
		bootstrapClient := bootstrapClusterProxy.GetClient()
		ListBareMetalHosts(ctx, bootstrapClient, client.InNamespace(namespace))
		WaitForNumBmhInState(ctx, bmov1alpha1.StateAvailable, WaitForNumInput{
			Client:    bootstrapClient,
			Options:   []client.ListOption{client.InNamespace(namespace)},
			Replicas:  5,
			Intervals: e2eConfig.GetIntervals(specName, "wait-bmh-available"),
		})
		ListBareMetalHosts(ctx, bootstrapClient, client.InNamespace(namespace))
	})
})

// preWaitForCluster is a hook function that should be called from ClusterctlUpgradeSpec before waiting for the cluster to spin up
// it creates the needed bmhs in namespace hosting the cluster and export the providerID format for v1alpha5.
func preWaitForCluster(clusterProxy framework.ClusterProxy, clusterNamespace string, clusterName string) {
	// Install the split-yaml tool to split our bmhosts_crs.yaml file.
	installSplitYAML := func() {
		cmd := exec.Command("bash", "-c", "kubectl krew update; kubectl krew install split-yaml")
		output, err := cmd.CombinedOutput()
		Logf("Download split-yaml:\n %v", string(output))
		Expect(err).To(BeNil())
	}

	// Split the <fileName> file into <resourceName>.yaml files based on the name of each resource and return list of the filename created
	// so we can apply limited number on BMH in each namespace.
	splitYAMLFile := func(fileName string, filePath string) []string {
		//#nosec G204 -- We need to pass in the file name here.
		cmd := exec.Command("bash", "-c", fmt.Sprintf("cat %s | kubectl split-yaml -t {{.name}}.yaml -p.", fileName))
		cmd.Dir = filePath
		output, err := cmd.CombinedOutput()
		Logf("splitting %s%s file into multiple files: \n %v", filePath, fileName, string(output))
		Expect(err).To(BeNil())
		return strings.Split(string(output), "\n")
	}

	// Create the BMHs needed in the hosting namespace.
	createBMH := func(clusterProxy framework.ClusterProxy, clusterNamespace string, clusterName string) {
		installSplitYAML()
		splitFiles := splitYAMLFile("bmhosts_crs.yaml", workDir)
		// Check which from which cluster creation this call is coming
		// if isBootstrapProxy==true then this call when creating the management else we are creating the workload.
		isBootstrapProxy := !strings.HasPrefix(clusterProxy.GetName(), "clusterctl-upgrade")
		if isBootstrapProxy {
			// remove existing bmh from source and apply first 2 in target
			Logf("remove existing bmh from source")
			cmd := exec.Command("bash", "-c", "kubectl delete -f bmhosts_crs.yaml  -n metal3")
			cmd.Dir = workDir
			output, err := cmd.CombinedOutput()
			Logf("Remove existing bmhs:\n %v", string(output))
			Expect(err).To(BeNil())

			// Apply secrets and bmhs for [node_0 and node_1] in the management cluster to host the target management cluster
			for i := 0; i < 4; i++ {
				resource, err := os.ReadFile(filepath.Join(workDir, splitFiles[i]))
				Expect(err).ShouldNot(HaveOccurred())
				Expect(clusterProxy.Apply(ctx, resource, []string{"-n", clusterNamespace}...)).ShouldNot(HaveOccurred())
			}
		} else {
			// Apply secrets and bmhs for [node_2, node_3 and node_4] in the management cluster to host workload cluster
			for i := 4; i < 10; i++ {
				resource, err := os.ReadFile(filepath.Join(workDir, splitFiles[i]))
				Expect(err).ShouldNot(HaveOccurred())
				Expect(clusterProxy.Apply(ctx, resource, []string{"-n", clusterNamespace}...)).ShouldNot(HaveOccurred())
			}
		}
	}

	// Create bmhs in the in the namespace that will host the new cluster
	createBMH(clusterProxy, clusterNamespace, clusterName)
}

// preInitFunc hook function that should be called from ClusterctlUpgradeSpec before init the management cluster
// it installs certManager, BMO and Ironic and overrides the default IPs for the workload cluster.
func preInitFunc(clusterProxy framework.ClusterProxy) {
	installCertManager := func(clusterProxy framework.ClusterProxy) {
		certManagerLink := fmt.Sprintf("https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml", config.CertManagerDefaultVersion)
		err := DownloadFile("/tmp/certManager.yaml", certManagerLink)
		Expect(err).To(BeNil(), "Unable to download certmanager manifest")
		certManagerYaml, err := os.ReadFile("/tmp/certManager.yaml")
		Expect(err).ShouldNot(HaveOccurred())
		Expect(clusterProxy.Apply(ctx, certManagerYaml)).ShouldNot(HaveOccurred())

		By("Wait for cert-manager pods to be available")
		deploymentNameList := []string{}
		deploymentNameList = append(deploymentNameList, "cert-manager", "cert-manager-cainjector", "cert-manager-webhook")
		clientSet := clusterProxy.GetClientSet()
		for _, name := range deploymentNameList {
			deployment, err := clientSet.AppsV1().Deployments("cert-manager").Get(ctx, name, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred(), "Unable to get the deployment %s in namespace %s \n error message: %s", name, "cert-manager", err)
			framework.WaitForDeploymentsAvailable(ctx, framework.WaitForDeploymentsAvailableInput{
				Getter:     clusterProxy.GetClient(),
				Deployment: deployment,
			}, e2eConfig.GetIntervals(specName, "wait-deployment")...)
		}
	}

	By("Fetch manifest for bootstrap cluster")
	path := filepath.Join(os.Getenv("CAPM3PATH"), "scripts")
	cmd := exec.Command("./fetch_manifests.sh") // #nosec G204:gosec
	cmd.Dir = path
	_ = cmd.Run()

	By("Fetch target cluster kubeconfig for target cluster log collection")
	kconfigPathWorkload := clusterProxy.GetKubeconfigPath()
	os.Setenv("KUBECONFIG_WORKLOAD", kconfigPathWorkload)
	Logf("Save kubeconfig in temp folder for project-infra target log collection")
	kubeconfigPathTemp := "/tmp/kubeconfig-test1.yaml"
	cmd = exec.Command("cp", kconfigPathWorkload, kubeconfigPathTemp) // #nosec G204:gosec
	stdoutStderr, er := cmd.CombinedOutput()
	Logf("%s\n", stdoutStderr)
	Expect(er).To(BeNil(), "Cannot fetch target cluster kubeconfig")
	// install certmanager
	installCertManager(clusterProxy)
	// Remove ironic
	By("Remove Ironic containers from the source cluster")
	ephemeralCluster := os.Getenv("EPHEMERAL_CLUSTER")
	isIronicDeployment := true
	if ephemeralCluster == Kind {
		isIronicDeployment = false
	}
	removeIronic(ctx, func() RemoveIronicInput {
		return RemoveIronicInput{
			ManagementCluster: bootstrapClusterProxy,
			IsDeployment:      isIronicDeployment,
			Namespace:         e2eConfig.GetVariable(ironicNamespace),
			NamePrefix:        e2eConfig.GetVariable(NamePrefix),
		}
	})

	// install ironic
	By("Install Ironic in the target cluster")
	installIronicBMO(ctx, func() installIronicBMOInput {
		return installIronicBMOInput{
			ManagementCluster:          clusterProxy,
			BMOPath:                    e2eConfig.GetVariable(bmoPath),
			deployIronic:               true,
			deployBMO:                  false,
			deployIronicTLSSetup:       getBool(e2eConfig.GetVariable(ironicTLSSetup)),
			deployIronicBasicAuth:      getBool(e2eConfig.GetVariable(ironicBasicAuth)),
			deployIronicKeepalived:     getBool(e2eConfig.GetVariable(ironicKeepalived)),
			deployIronicMariadb:        getBool(e2eConfig.GetVariable(ironicMariadb)),
			Namespace:                  e2eConfig.GetVariable(ironicNamespace),
			NamePrefix:                 e2eConfig.GetVariable(NamePrefix),
			RestartContainerCertUpdate: getBool(e2eConfig.GetVariable(restartContainerCertUpdate)),
			E2EConfig:                  e2eConfig,
			SpecName:                   specName,
		}
	})

	// install bmo
	By("Install BMO")
	installIronicBMO(ctx, func() installIronicBMOInput {
		return installIronicBMOInput{
			ManagementCluster:          clusterProxy,
			BMOPath:                    e2eConfig.GetVariable(bmoPath),
			deployIronic:               false,
			deployBMO:                  true,
			deployIronicTLSSetup:       getBool(e2eConfig.GetVariable(ironicTLSSetup)),
			deployIronicBasicAuth:      getBool(e2eConfig.GetVariable(ironicBasicAuth)),
			deployIronicKeepalived:     getBool(e2eConfig.GetVariable(ironicKeepalived)),
			deployIronicMariadb:        getBool(e2eConfig.GetVariable(ironicMariadb)),
			Namespace:                  e2eConfig.GetVariable(ironicNamespace),
			NamePrefix:                 e2eConfig.GetVariable(NamePrefix),
			RestartContainerCertUpdate: getBool(e2eConfig.GetVariable(restartContainerCertUpdate)),
			E2EConfig:                  e2eConfig,
			SpecName:                   specName,
		}
	})

	// Export capi/capm3 versions
	os.Setenv("CAPI_VERSION", "v1alpha4")
	os.Setenv("CAPM3_VERSION", "v1alpha5")

	// These exports bellow we need them after applying the management cluster template and before
	// applying the workload. if exported before it will break creating the management because it uses v1beta1 templates and default IPs.
	// override the provider id format
	os.Setenv("PROVIDER_ID_FORMAT", "metal3://{{ ds.meta_data.uuid }}")
	// override default IPs for the workload cluster
	os.Setenv("CLUSTER_APIENDPOINT_HOST", "192.168.111.250")
	os.Setenv("IPAM_EXTERNALV4_POOL_RANGE_START", "192.168.111.201")
	os.Setenv("IPAM_EXTERNALV4_POOL_RANGE_END", "192.168.111.240")
	os.Setenv("IPAM_PROVISIONING_POOL_RANGE_START", "172.22.0.201")
	os.Setenv("IPAM_PROVISIONING_POOL_RANGE_END", "172.22.0.240")
}

// preUpgrade hook should be called from ClusterctlUpgradeSpec before upgrading the management cluster
// it upgrades Ironic and BMO before upgrading the providers.
func preUpgrade(clusterProxy framework.ClusterProxy) {
	upgradeIronic(ctx, func() upgradeIronicInput {
		return upgradeIronicInput{
			E2EConfig:         e2eConfig,
			ManagementCluster: clusterProxy,
			SpecName:          specName,
		}
	})
	upgradeBMO(ctx, func() upgradeBMOInput {
		return upgradeBMOInput{
			E2EConfig:         e2eConfig,
			ManagementCluster: clusterProxy,
			SpecName:          specName,
		}
	})
}

// preCleanupManagementCluster hook should be called from ClusterctlUpgradeSpec before cleaning the target management cluster
// it moves back Ironic to the bootstrap cluster.
func preCleanupManagementCluster(clusterProxy framework.ClusterProxy) {
	// Abort the test in case of failure and keepTestEnv is true during keep VM trigger
	if CurrentSpecReport().Failed() {
		// Fetch logs in case of failure in management cluster
		By("Fetch logs from management cluster")
		path := filepath.Join(os.Getenv("CAPM3PATH"), "scripts")
		cmd := exec.Command("./fetch_target_logs.sh") // #nosec G204:gosec
		cmd.Dir = path
		errorPipe, _ := cmd.StderrPipe()
		_ = cmd.Start()
		errorData, _ := io.ReadAll(errorPipe)
		if len(errorData) > 0 {
			Logf("Error of the shell: %v\n", string(errorData))
		}

		if keepTestEnv {
			AbortSuite("e2e test aborted and skip cleaning the VM", 4)
		}
	}
	// Fetch logs from management cluster
	By("Fetch logs from management cluster")
	path := filepath.Join(os.Getenv("CAPM3PATH"), "scripts")
	cmd := exec.Command("./fetch_target_logs.sh") //#nosec G204:gosec
	cmd.Dir = path
	errorPipe, _ := cmd.StderrPipe()
	_ = cmd.Start()
	errorData, _ := io.ReadAll(errorPipe)
	if len(errorData) > 0 {
		Logf("Error of the shell: %v\n", string(errorData))
	}
	os.Unsetenv("KUBECONFIG_WORKLOAD")
	os.Unsetenv("KUBECONFIG_BOOTSTRAP")

	// Reinstall ironic
	reInstallIronic := func() {
		By("Reinstate Ironic containers and BMH")
		ephemeralCluster := os.Getenv("EPHEMERAL_CLUSTER")
		if ephemeralCluster == Kind {
			By("Install Ironic in the source cluster as containers")
			bmoPath := e2eConfig.GetVariable("BMOPATH")
			ironicCommand := bmoPath + "/tools/run_local_ironic.sh"
			//#nosec G204 -- We take the BMOPATH from a variable.
			cmd := exec.Command("sh", "-c", "export CONTAINER_RUNTIME=docker; "+ironicCommand)
			stdoutStderr, err := cmd.CombinedOutput()
			fmt.Printf("%s\n", stdoutStderr)
			Expect(err).To(BeNil(), "Cannot run local ironic")
		} else {
			By("Install Ironic in the source cluster as deployments")
			installIronicBMO(ctx, func() installIronicBMOInput {
				return installIronicBMOInput{
					ManagementCluster:          bootstrapClusterProxy,
					BMOPath:                    e2eConfig.GetVariable(bmoPath),
					deployIronic:               true,
					deployBMO:                  false,
					deployIronicTLSSetup:       getBool(e2eConfig.GetVariable(ironicTLSSetup)),
					deployIronicBasicAuth:      getBool(e2eConfig.GetVariable(ironicBasicAuth)),
					deployIronicKeepalived:     getBool(e2eConfig.GetVariable(ironicKeepalived)),
					deployIronicMariadb:        getBool(e2eConfig.GetVariable(ironicMariadb)),
					Namespace:                  e2eConfig.GetVariable(ironicNamespace),
					NamePrefix:                 e2eConfig.GetVariable(NamePrefix),
					RestartContainerCertUpdate: getBool(e2eConfig.GetVariable(restartContainerCertUpdate)),
					E2EConfig:                  e2eConfig,
					SpecName:                   specName,
				}
			})
		}
	}
	removeIronic(ctx, func() RemoveIronicInput {
		return RemoveIronicInput{
			ManagementCluster: clusterProxy,
			IsDeployment:      true,
			Namespace:         e2eConfig.GetVariable(ironicNamespace),
			NamePrefix:        e2eConfig.GetVariable(NamePrefix),
		}
	})
	reInstallIronic()

	// Clean env variables set for management upgrade, defaults are set in e2e config file
	// Capi/capm3 versions
	os.Unsetenv("CAPI_VERSION")
	os.Unsetenv("CAPM3_VERSION")
	// The provider id format
	os.Unsetenv("PROVIDER_ID_FORMAT")
	// IPs
	os.Unsetenv("CLUSTER_APIENDPOINT_HOST")
	os.Unsetenv("IPAM_EXTERNALV4_POOL_RANGE_START")
	os.Unsetenv("IPAM_EXTERNALV4_POOL_RANGE_END")
	os.Unsetenv("IPAM_PROVISIONING_POOL_RANGE_START")
	os.Unsetenv("IPAM_PROVISIONING_POOL_RANGE_END")
}
