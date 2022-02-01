package e2e

import (
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client/config"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
)

func upgradeManagementCluster() {
	Logf("Starting v1a5 to v1b1 upgrade tests")
	var (
		specName            = "clusterctl-upgrade"
		upgradeClusterProxy framework.ClusterProxy
	)
	upgradeClusterProxy = bootstrapClusterProxy.GetWorkloadCluster(ctx, cluster.Namespace, cluster.Name)

	clusterctlBinaryURLTemplate := e2eConfig.GetVariable("INIT_WITH_BINARY")
	clusterctlBinaryURLReplacer := strings.NewReplacer("{OS}", runtime.GOOS, "{ARCH}", runtime.GOARCH)
	clusterctlBinaryURL := clusterctlBinaryURLReplacer.Replace(clusterctlBinaryURLTemplate)

	Logf("Downloading clusterctl binary from %s", clusterctlBinaryURL)
	clusterctlBinaryPath := downloadToTmpFile(clusterctlBinaryURL)
	defer os.Remove(clusterctlBinaryPath) // clean up

	err := os.Chmod(clusterctlBinaryPath, 0744) //nolint:gosec
	Expect(err).ToNot(HaveOccurred(), "failed to chmod temporary file")

	By("Initializing the workload cluster with older versions of providers")

	contract := e2eConfig.GetVariable("UPGRADE_FROM_CAPI_VERSION")

	clusterctl.InitManagementClusterAndWatchControllerLogs(ctx, clusterctl.InitManagementClusterAndWatchControllerLogsInput{
		ClusterctlBinaryPath:    clusterctlBinaryPath, // use older version of clusterctl to init the management cluster
		ClusterProxy:            targetCluster,
		ClusterctlConfigPath:    clusterctlConfigPath,
		CoreProvider:            e2eConfig.GetProviderLatestVersionsByContract(contract, config.ClusterAPIProviderName)[0],
		BootstrapProviders:      e2eConfig.GetProviderLatestVersionsByContract(contract, config.KubeadmBootstrapProviderName),
		ControlPlaneProviders:   e2eConfig.GetProviderLatestVersionsByContract(contract, config.KubeadmControlPlaneProviderName),
		InfrastructureProviders: e2eConfig.GetProviderLatestVersionsByContract(contract, e2eConfig.InfrastructureProviders()...),
		LogFolder:               filepath.Join(artifactFolder, "clusters", cluster.Name),
	}, e2eConfig.GetIntervals(specName, "wait-controllers")...)

	By("Get the management cluster images before upgrading")
	targetClusterClientSet := targetCluster.GetClientSet()
	pods, err := targetClusterClientSet.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	Expect(err).To(BeNil())
	printImages(pods)

	By("Upgrading providers to the latest version available")
	pwd, err := os.Getwd()
	Expect(err).To(BeNil())
	Logf("PWD:", pwd)

	type vars struct {
		HOME                 string
		CAPM3_REL_TO_VERSION string
	}
	home, err := os.UserHomeDir()
	Expect(err).To(BeNil())
	templateVars := vars{home, e2eConfig.GetVariable("CAPM3_REL_TO_VERSION")}

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
		LogFolder:            filepath.Join(artifactFolder, "clusters", cluster.Name),
	}, e2eConfig.GetIntervals(specName, "wait-controllers")...)

	By("THE MANAGEMENT CLUSTER WAS SUCCESSFULLY UPGRADED!")

	By("Get the management cluster images after upgrading")
	pods, err = targetClusterClientSet.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
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
