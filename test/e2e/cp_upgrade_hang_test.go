package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	ipamv1 "github.com/metal3-io/ip-address-manager/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	cpUpgradeHangPollInterval   = 20 * time.Second
	cpUpgradeHangDefaultTimeout = 60 * time.Minute
)

var _ = Describe("Control plane upgrade hang soak", Label("cp-upgrade-hang"), func() {
	ctx := context.Background()
	var clusterctlLogFolder string

	BeforeEach(func() {
		osType = strings.ToLower(os.Getenv("OS"))
		Expect(osType).ToNot(Equal(""))
		validateGlobals(specName)

		// We need to override clusterctl apply log folder to avoid getting our credentials exposed.
		clusterctlLogFolder = filepath.Join(os.TempDir(), "target_cluster_logs", bootstrapClusterProxy.GetName())
	})

	It("rolls the control plane until an IPClaim or machine gets stuck", func() {
		Expect(os.Getenv("ENABLE_BMH_NAME_BASED_PREALLOCATION")).To(Equal("true"), "ENABLE_BMH_NAME_BASED_PREALLOCATION=true is required for this race")
		Expect(numberOfControlplane).To(BeNumerically(">=", 3), "the soak works better when there are multiple control-plane replicas")

		k8sversion := e2eConfig.MustGetVariable("KUBERNETES_VERSION")
		imageURL, imageChecksum := EnsureImage(e2eConfig, k8sversion)

		By("Apply BMHs for the workload cluster")
		ApplyBmh(ctx, e2eConfig, bootstrapClusterProxy, namespace, specName)

		By("Create the control-plane-only workload cluster")
		targetCluster, _ = CreateTargetCluster(ctx, func() CreateTargetClusterInput {
			return CreateTargetClusterInput{
				E2EConfig:             e2eConfig,
				BootstrapClusterProxy: bootstrapClusterProxy,
				SpecName:              specName,
				ClusterName:           clusterName,
				K8sVersion:            k8sversion,
				KCPMachineCount:       int64(numberOfControlplane),
				WorkerMachineCount:    int64(numberOfWorkers),
				ClusterctlLogFolder:   clusterctlLogFolder,
				ClusterctlConfigPath:  clusterctlConfigPath,
				OSType:                osType + flavorSuffix(),
				Namespace:             namespace,
			}
		})

		clusterClient := bootstrapClusterProxy.GetClient()
		// Enable node reuse on the initial control-plane template too, so the BMHs
		// provisioned for the first set of CP machines are eligible for reuse when
		// the first scale-in roll deprovisions them. Each per-iteration template
		// created below also gets nodeReuse=true before the KCP rolls onto it.
		controlPlaneTemplate := clusterName + "-controlplane"
		updateNodeReuse(ctx, namespace, true, controlPlaneTemplate, clusterClient)
		WaitForNumMachines(ctx, cpMachineIsReady(k8sversion), WaitForNumInput{
			Client:    clusterClient,
			Options:   cpHangMachineListOptions(namespace, clusterName),
			Replicas:  numberOfControlplane,
			Intervals: e2eConfig.GetIntervals(specName, "wait-machine-running"),
		})

		staleClaim, err := findStaleCPHangIPClaim(ctx, clusterClient, namespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(staleClaim).To(BeEmpty(), "found a stale IPClaim before starting the soak: %s", staleClaim)

		for iteration := 1; ; iteration++ {
			Logf("=== soak iteration %d: replacing CP machines ===", iteration)

			// Capture the current control-plane machines so we can tell when the
			// scale-in roll has fully replaced them. Requiring every pre-roll machine
			// to be gone proves a fresh roll happened.
			preRollMachines, err := listCPHangMachineNames(ctx, clusterClient, namespace, clusterName)
			Expect(err).NotTo(HaveOccurred())

			kcpObj := framework.GetKubeadmControlPlaneByCluster(ctx, framework.GetKubeadmControlPlaneByClusterInput{
				Lister:      clusterClient,
				ClusterName: clusterName,
				Namespace:   namespace,
			})
			oldTemplateName := kcpObj.Spec.MachineTemplate.Spec.InfrastructureRef.Name
			newTemplateName := fmt.Sprintf("%s-cp-soak-%d", clusterName, iteration)
			CreateNewM3MachineTemplate(ctx, namespace, newTemplateName, oldTemplateName, clusterClient, imageURL, imageChecksum)
			// The roll uses MaxSurge=0 (scale-in): KCP deletes an old CP machine
			// before creating its replacement, so the replacement has no spare BMH
			// to provision onto unless the just-deprovisioned BMH is reused. Node
			// reuse must therefore be enabled on the template the KCP actually rolls
			// onto this iteration, not only on the original "-controlplane" template.
			// Without this the scale-in roll stalls because there is nowhere to
			// place the replacement machine.
			updateNodeReuse(ctx, namespace, true, newTemplateName, clusterClient)

			helper, err := patch.NewHelper(kcpObj, clusterClient)
			Expect(err).NotTo(HaveOccurred())
			kcpObj.Spec.MachineTemplate.Spec.InfrastructureRef.Name = newTemplateName
			// Force delete-first (scale-in) rollout ordering. KCP's rollout
			// strategy exposes only MaxSurge (there is no MaxUnavailable field on
			// a KubeadmControlPlane — that field exists only on MachineDeployment
			// rollouts). MaxSurge=0 makes KCP delete an outdated control-plane
			// Machine BEFORE creating its replacement, i.e. delete-clear-recreate
			// ordering, so no spare BMH is ever needed. MaxSurge=1 (the default)
			// would instead surge to N+1 first, which deadlocks here because there
			// is no spare BMH for the extra Machine to associate with. Assign the
			// whole value (not just IntVal) so it is correct even if MaxSurge was
			// unset/omitempty on the fetched object.
			kcpObj.Spec.Rollout.Strategy.RollingUpdate.MaxSurge = ptr.To(intstr.FromInt32(0))
			// On a control-plane-only cluster with no spare BMHs the MaxSurge=0
			// scale-in roll deletes a CP machine before its replacement comes up,
			// temporarily dropping capacity. The default control-plane NoSchedule
			// taint can wedge the roll because nothing can be rescheduled onto the
			// remaining tainted CP nodes while quorum is reduced. Clearing the CP
			// taints during the roll lets the scale-in proceed and the reused BMH
			// come back as the replacement. This mirrors the proven node_reuse CP
			// roll (see node_reuse.go), which runs the same MaxSurge=0 scale-in on
			// a 3-node control plane.
			kcpObj.Spec.KubeadmConfigSpec.JoinConfiguration.NodeRegistration.Taints = ptr.To([]corev1.Taint{})
			kcpObj.Spec.KubeadmConfigSpec.InitConfiguration.NodeRegistration.Taints = ptr.To([]corev1.Taint{})
			Expect(helper.Patch(ctx, kcpObj)).To(Succeed())

			completed, hangReason, err := waitForCPHangRoll(ctx, clusterClient, namespace, clusterName, k8sversion, preRollMachines, numberOfControlplane, cpUpgradeHangDefaultTimeout)
			Expect(err).NotTo(HaveOccurred())
			if !completed {
				Logf("HANG DETECTED on iteration %d: %s", iteration, hangReason)
				Logf("Successful rolls before hang: %d", iteration-1)
				// Keep both the workload cluster and management cluster for inspection.
				skipCleanup = true
				captureCPHangArtifacts(iteration)
				return
			}

			Logf("iteration %d OK (no hang)", iteration)
		}
	})

	AfterEach(func() {
		DumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, targetCluster, artifactFolder, namespace, e2eConfig.GetIntervals, clusterName, clusterctlLogFolder, skipCleanup, clusterctlConfigPath)
	})
})

func cpHangMachineListOptions(namespace, clusterName string) []client.ListOption {
	return []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{
			clusterv1.ClusterNameLabel:         clusterName,
			clusterv1.MachineControlPlaneLabel: "",
		},
	}
}

// cpMachineIsReady accepts a control-plane Machine that is fully Running at
// the expected Kubernetes version with a joined Node.
func cpMachineIsReady(version string) func(clusterv1.Machine) bool {
	return func(machine clusterv1.Machine) bool {
		_, isControlPlane := machine.GetLabels()[clusterv1.MachineControlPlaneLabel]
		return isControlPlane &&
			machine.Status.GetTypedPhase() == clusterv1.MachinePhaseRunning &&
			machine.Status.NodeRef.Name != "" &&
			machine.Spec.Version == version
	}
}

func findStaleCPHangIPClaim(ctx context.Context, clusterClient client.Client, namespace string) (string, error) {
	claims := ipamv1.IPClaimList{}
	if err := clusterClient.List(ctx, &claims, client.InNamespace(namespace)); err != nil {
		return "", fmt.Errorf("list IPClaims: %w", err)
	}

	for _, claim := range claims.Items {
		if claim.Status.Address == nil || claim.Status.Address.Name == "" {
			continue
		}
		addressNamespace := claim.Status.Address.Namespace
		if addressNamespace == "" {
			addressNamespace = claim.Namespace
		}
		address := ipamv1.IPAddress{}
		err := clusterClient.Get(ctx, client.ObjectKey{Namespace: addressNamespace, Name: claim.Status.Address.Name}, &address)
		if apierrors.IsNotFound(err) {
			return fmt.Sprintf("%s/%s references missing IPAddress %s/%s", claim.Namespace, claim.Name, addressNamespace, claim.Status.Address.Name), nil
		}
		if err != nil {
			return "", fmt.Errorf("get IPAddress %s/%s for IPClaim %s/%s: %w", addressNamespace, claim.Status.Address.Name, claim.Namespace, claim.Name, err)
		}
	}

	return "", nil
}

func listCPHangMachineNames(ctx context.Context, clusterClient client.Client, namespace, clusterName string) (map[string]struct{}, error) {
	machines := clusterv1.MachineList{}
	if err := clusterClient.List(ctx, &machines, cpHangMachineListOptions(namespace, clusterName)...); err != nil {
		return nil, fmt.Errorf("list control-plane Machines: %w", err)
	}
	names := make(map[string]struct{}, len(machines.Items))
	for _, machine := range machines.Items {
		names[machine.Name] = struct{}{}
	}
	return names, nil
}

func waitForCPHangRoll(ctx context.Context, clusterClient client.Client, namespace, clusterName, version string, preRollMachines map[string]struct{}, replicas int, timeout time.Duration) (bool, string, error) {
	ticker := time.NewTicker(cpUpgradeHangPollInterval)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	accept := cpMachineIsReady(version)
	// progressed tracks whether the scale-in roll ever actually did anything:
	// a pre-roll machine entering Deleting, or any machine appearing in
	// Provisioning. If the timeout fires without progress, the roll never
	// started (e.g. no BMH available to place the replacement) and that is a
	// misconfiguration, NOT the stale-IPClaim race this soak hunts for.
	progressed := false
	for {
		staleClaim, err := findStaleCPHangIPClaim(ctx, clusterClient, namespace)
		if err != nil {
			return false, "", err
		}
		if staleClaim != "" {
			return false, "stale IPClaim detected: " + staleClaim, nil
		}

		machines := clusterv1.MachineList{}
		if err := clusterClient.List(ctx, &machines, cpHangMachineListOptions(namespace, clusterName)...); err != nil {
			return false, "", fmt.Errorf("list control-plane Machines: %w", err)
		}
		ready := 0
		preRollRemaining := 0
		provisioning := 0
		for _, machine := range machines.Items {
			if _, existedBefore := preRollMachines[machine.Name]; existedBefore {
				preRollRemaining++
			}
			switch machine.Status.GetTypedPhase() {
			case clusterv1.MachinePhaseDeleting, clusterv1.MachinePhaseProvisioning:
				// Either an old machine being torn down or a replacement coming
				// up means the roll is underway.
				progressed = true
			}
			if machine.Status.GetTypedPhase() == clusterv1.MachinePhaseProvisioning {
				provisioning++
			}
			if accept(machine) {
				ready++
			}
		}
		// A replacement that existed before is gone (preRollRemaining dropped)
		// also counts as progress.
		if preRollRemaining < len(preRollMachines) {
			progressed = true
		}
		// The scale-in roll is complete only when every replica is Running at the
		// target version AND none of the machines that existed before the patch
		// remain. The second condition is what distinguishes a real roll from the
		// pre-roll steady state on same-version iterations.
		if ready == replicas && preRollRemaining == 0 {
			return true, "", nil
		}

		select {
		case <-ctx.Done():
			return false, "", ctx.Err()
		case <-timer.C:
			if !progressed {
				// Nothing ever moved: no machine entered Deleting/Provisioning and
				// no pre-roll machine was removed. This is a stuck/no-op roll, not
				// the stale-IPClaim race. Flag it distinctly so it is not mistaken
				// for a reproduction of the bug (common cause: MaxSurge=0 scale-in
				// with no spare BMH and node reuse not effective on the template).
				return false, fmt.Sprintf("roll never progressed: %d/%d control-plane Machines Running at version %s, all %d pre-roll machines still present, 0 in Provisioning within %s — the scale-in roll did not start (check spare BMH capacity and that nodeReuse is set on the rolled template); this is NOT the stale-IPClaim hang", ready, replicas, version, preRollRemaining, timeout), nil
			}
			return false, fmt.Sprintf("%d/%d control-plane Machines reached Running with a NodeRef at version %s (%d pre-roll machines remaining, %d in Provisioning) within %s", ready, replicas, version, preRollRemaining, provisioning, timeout), nil
		case <-ticker.C:
		}
	}
}

func captureCPHangArtifacts(iteration int) {
	outputFolder := filepath.Join(artifactFolder, "cp-upgrade-hang", fmt.Sprintf("iteration-%d", iteration))
	for _, proxy := range []framework.ClusterProxy{bootstrapClusterProxy, targetCluster} {
		if proxy == nil {
			continue
		}
		if err := FetchClusterLogs(proxy, filepath.Join(outputFolder, "logs")); err != nil {
			Logf("failed to fetch logs for %s: %v", proxy.GetName(), err)
		}
		if err := FetchManifests(proxy, filepath.Join(outputFolder, "manifests")); err != nil {
			Logf("failed to fetch manifests for %s: %v", proxy.GetName(), err)
		}
	}
}
