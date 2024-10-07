package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("When testing basic cluster creation [basic]", Label("basic"), func() {
	BeforeEach(func() {
		osType := strings.ToLower(os.Getenv("OS")) + "-fake"
		Expect(osType).ToNot(Equal(""))
		validateGlobals(specName)

		// We need to override clusterctl apply log folder to avoid getting our credentials exposed.
		clusterctlLogFolder = filepath.Join(os.TempDir(), "clusters", bootstrapClusterProxy.GetName())
		// FKASDeployLogFolder := filepath.Join(os.TempDir(), "fkas-deploy-logs", bootstrapClusterProxy.GetName())
		FKASKustomization := e2eConfig.GetVariable("FKAS_RELEASE_LATEST")
		By(fmt.Sprintf("Installing FKAS from kustomization %s on the bootsrap cluster", FKASKustomization))
		// err := BuildAndApplyKustomization(ctx, &BuildAndApplyKustomizationInput{
		// 	Kustomization:       FKASKustomization,
		// 	ClusterProxy:        bootstrapClusterProxy,
		// 	WaitForDeployment:   true,
		// 	WatchDeploymentLogs: true,
		// 	LogPath:             FKASDeployLogFolder,
		// 	DeploymentName:      "metal3-fake-api-server",
		// 	DeploymentNamespace: "metal3",
		// 	WaitIntervals:       e2eConfig.GetIntervals("default", "wait-deployment"),
		// })
		// Expect(err).NotTo(HaveOccurred())
	})

	It("Should create a workload cluster", func() {
		By("Fetching cluster configuration")
		k8sVersion := e2eConfig.GetVariable("KUBERNETES_VERSION")
		By("Provision Workload cluster")
		targetCluster, _ = createFakeTargetCluster(k8sVersion)
	})

	AfterEach(func() {
		// RemoveDeployment(ctx, func() RemoveDeploymentInput {
		// 	return RemoveDeploymentInput{
		// 		ManagementCluster: bootstrapClusterProxy,
		// 		Namespace:         "metal3",
		// 		Name:              "metal3-fake-api-server",
		// 	}
		// })
		DumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, e2eConfig.GetIntervals, clusterName, clusterctlLogFolder, skipCleanup)
	})
})
