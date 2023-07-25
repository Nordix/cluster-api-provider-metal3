package e2e

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
)

const (
	rebootAnnotation    = "reboot.metal3.io"
	poweroffAnnotation  = "reboot.metal3.io/poweroff"
	unhealthyAnnotation = "capi.metal3.io/unhealthy"
	defaultNamespace    = "default"
)

type RemediationInput struct {
	E2EConfig             *clusterctl.E2EConfig
	BootstrapClusterProxy framework.ClusterProxy
	TargetCluster         framework.ClusterProxy
	SpecName              string
	ClusterName           string
	Namespace             string
	ClusterctlConfigPath  string
}

/*
WIP
*/
func healthcheck(ctx context.Context, inputGetter func() RemediationInput) {
	Logf("Starting healthcheck tests")
	Logf("Applying the machine healthcheck worker template yaml to the cluster")
	Expect(managementClusterProxy.Apply(ctx, workloadClusterTemplate)).To(Succeed())

	By("REMEDIATION TESTS PASSED!")
}
