package e2e

import (
	"os"
	"path/filepath"
	"fmt"
	"strconv"

	bmov1alpha1 "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

/*
 * TBD
 */

var _ = Describe("When testing BMO scalability with fakeIPA [bmo-scalability]", Label("bmo-scalability"), func() {
	bmoScalabilityTest()
})


func bmoScalabilityTest() {
	BeforeEach(func() {
		validateGlobals(specName)
		// We need to override clusterctl apply log folder to avoid getting our credentials exposed.
		clusterctlLogFolder = filepath.Join(os.TempDir(), "clusters", bootstrapClusterProxy.GetName())
	})
	It("Should wait for BMH be available and provision them", func() {
		//fakeImageURL := e2eConfig.GetVariable("FAKE_IMAGE")
		num_nodes, _ := strconv.Atoi(e2eConfig.GetVariable("NUM_NODES"))
		batch, _ := strconv.Atoi(e2eConfig.GetVariable("BMO_BATCH"))
		Logf("Starting BMO scalability test")
		bootstrapClient := bootstrapClusterProxy.GetClient()
		ListBareMetalHosts(ctx, bootstrapClient, client.InNamespace(namespace))
		apply_batch_bmh := func (from int, to int) {
			Logf("Apply BMH batch from node_%d to node_%d", from, to)
			for i:= from; i < to+1; i++ {
				resource, err := os.ReadFile(filepath.Join(workDir, fmt.Sprintf("bmhs/node_%d.yaml", i)))
				Expect(err).ShouldNot(HaveOccurred())
				Expect(CreateOrUpdateWithNamespace(ctx, bootstrapClusterProxy, resource, namespace)).ShouldNot(HaveOccurred())
			}
			Logf("Wait for batch from node_%d to node_%d to become available", from, to)
			WaitForNumBmhInState(ctx, bmov1alpha1.StateAvailable, WaitForNumInput{
				Client:    bootstrapClient,
				Options:   []client.ListOption{client.InNamespace(namespace)},
				Replicas:  to+1,
				Intervals: e2eConfig.GetIntervals(specName, "wait-bmh-available"),
			})
		}
		
		for i:=0; i < num_nodes; i+=batch {
			if i+batch > num_nodes {
				apply_batch_bmh(i, num_nodes-1)
				break
			}
			apply_batch_bmh(i, i+batch-1)
		}

		By("BMO SCALABILITY TEST PASSED!")
	})
}
