package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	bmov1alpha1 "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	infrav1 "github.com/metal3-io/cluster-api-provider-metal3/api/v1beta1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	controlplanev1 "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1beta1"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Byf(format string, a ...interface{}) {
	By(fmt.Sprintf(format, a...))
}

func Logf(format string, a ...interface{}) {
	fmt.Fprintf(GinkgoWriter, "INFO: "+format+"\n", a...)
}

func LogFromFile(logFile string) {
	data, err := os.ReadFile(filepath.Clean(logFile))
	Expect(err).To(BeNil(), "No log file found")
	Logf(string(data))
}

func dumpSpecResourcesAndCleanup(ctx context.Context, specName string, clusterProxy framework.ClusterProxy, artifactFolder string, namespace string, intervalsGetter func(spec, key string) []interface{}, clusterName, clusterctlLogFolder string, skipCleanup bool) {
	Expect(os.RemoveAll(clusterctlLogFolder)).Should(Succeed())
	client := clusterProxy.GetClient()

	// Dumps all the resources in the spec namespace, then cleanups the cluster object and the spec namespace itself.
	By(fmt.Sprintf("Dumping all the Cluster API resources in the %q namespace", namespace))
	// Dump all Cluster API related resources to artifacts before deleting them.
	framework.DumpAllResources(ctx, framework.DumpAllResourcesInput{
		Lister:    client,
		Namespace: namespace,
		LogPath:   filepath.Join(artifactFolder, "clusters", clusterProxy.GetName(), "resources"),
	})

	if !skipCleanup {
		By(fmt.Sprintf("Deleting cluster %s/%s", namespace, clusterName))
		// While https://github.com/kubernetes-sigs/cluster-api/issues/2955 is addressed in future iterations, there is a chance
		// that cluster variable is not set even if the cluster exists, so we are calling DeleteAllClustersAndWait
		// instead of DeleteClusterAndWait
		framework.DeleteAllClustersAndWait(ctx, framework.DeleteAllClustersAndWaitInput{
			Client:    client,
			Namespace: namespace,
		}, intervalsGetter(specName, "wait-delete-cluster")...)
	}
}

// downloadFile will download a url and store it in local filepath.
func downloadFile(filePath string, url string) error {
	// Get the data
	resp, err := http.Get(url) //nolint:noctx // NB: as we're just implementing an external interface we won't be able to get a context here.
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Create the file
	out, err := os.Create(filepath.Clean(filePath))
	if err != nil {
		return err
	}
	defer func() {
		err := out.Close()
		Expect(err).To(BeNil(), "Error closing file")
	}()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	return err
}

// filterBmhsByProvisioningState returns a filtered list of BaremetalHost objects in certain provisioning state.
func filterBmhsByProvisioningState(bmhs []bmov1alpha1.BareMetalHost, state bmov1alpha1.ProvisioningState) (result []bmov1alpha1.BareMetalHost) {
	for _, bmh := range bmhs {
		if bmh.Status.Provisioning.State == state {
			result = append(result, bmh)
		}
	}
	return
}

// filterMachinesByPhase returns a filtered list of CAPI machine objects in certain desired phase.
func filterMachinesByPhase(machines []clusterv1.Machine, phase clusterv1.MachinePhase) (result []clusterv1.Machine) {
	accept := func(machine clusterv1.Machine) bool {
		return machine.Status.GetTypedPhase() == phase
	}
	return filterMachines(machines, accept)
}

// filterMachines returns a filtered list of Machines that were accepted by the accept function.
func filterMachines(machines []clusterv1.Machine, accept func(clusterv1.Machine) bool) (result []clusterv1.Machine) {
	for _, machine := range machines {
		if accept(machine) {
			result = append(result, machine)
		}
	}
	return
}

// annotateBmh annotates BaremetalHost with a given key and value.
func annotateBmh(ctx context.Context, client client.Client, host bmov1alpha1.BareMetalHost, key string, value *string) {
	helper, err := patch.NewHelper(&host, client)
	Expect(err).NotTo(HaveOccurred())
	annotations := host.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	if value == nil {
		delete(annotations, key)
	} else {
		annotations[key] = *value
	}
	host.SetAnnotations(annotations)
	Expect(helper.Patch(ctx, &host)).To(Succeed())
}

// deleteNodeReuseLabelFromHost deletes nodeReuseLabelName from the host if it exists.
func deleteNodeReuseLabelFromHost(ctx context.Context, client client.Client, host bmov1alpha1.BareMetalHost, nodeReuseLabelName string) {
	helper, err := patch.NewHelper(&host, client)
	Expect(err).NotTo(HaveOccurred())
	labels := host.GetLabels()
	if labels != nil {
		if _, ok := labels[nodeReuseLabelName]; ok {
			delete(host.Labels, nodeReuseLabelName)
		}
	}
	Expect(helper.Patch(ctx, &host)).To(Succeed())
}

// scaleMachineDeployment scales up/down MachineDeployment object to desired replicas.
func scaleMachineDeployment(ctx context.Context, clusterClient client.Client, clusterName, namespace string, newReplicas int) {
	machineDeployments := framework.GetMachineDeploymentsByCluster(ctx, framework.GetMachineDeploymentsByClusterInput{
		Lister:      clusterClient,
		ClusterName: clusterName,
		Namespace:   namespace,
	})
	Expect(len(machineDeployments)).To(Equal(1), "Expected exactly 1 MachineDeployment")
	machineDeploy := machineDeployments[0]
	patch := []byte(fmt.Sprintf(`{"spec": {"replicas": %d}}`, newReplicas))
	err := clusterClient.Patch(ctx, machineDeploy, client.RawPatch(types.MergePatchType, patch))
	Expect(err).To(BeNil(), "Failed to patch workers MachineDeployment")
}

// scaleKubeadmControlPlane scales up/down KubeadmControlPlane object to desired replicas.
func scaleKubeadmControlPlane(ctx context.Context, c client.Client, name client.ObjectKey, newReplicaCount int) {
	ctrlplane := controlplanev1.KubeadmControlPlane{}
	Expect(c.Get(ctx, name, &ctrlplane)).To(Succeed())
	helper, err := patch.NewHelper(&ctrlplane, c)
	Expect(err).To(BeNil(), "Failed to create new patch helper")

	ctrlplane.Spec.Replicas = pointer.Int32Ptr(int32(newReplicaCount))
	Expect(helper.Patch(ctx, &ctrlplane)).To(Succeed())
}

func deploymentRolledOut(ctx context.Context, clientSet *kubernetes.Clientset, name string, namespace string, desiredGeneration int64) bool {
	deploy, err := clientSet.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	Expect(err).To(BeNil())
	if deploy != nil {
		// When the number of replicas is equal to the number of available and updated
		// replicas, we know that only "new" pods are running. When we also
		// have the desired number of replicas and a new enough generation, we
		// know that the rollout is complete.
		return (deploy.Status.UpdatedReplicas == *deploy.Spec.Replicas) &&
			(deploy.Status.AvailableReplicas == *deploy.Spec.Replicas) &&
			(deploy.Status.Replicas == *deploy.Spec.Replicas) &&
			(deploy.Status.ObservedGeneration >= desiredGeneration)
	}
	return false
}

func getAllBmhs(ctx context.Context, c client.Client, namespace string) ([]bmov1alpha1.BareMetalHost, error) {
	bmhs := bmov1alpha1.BareMetalHostList{}
	err := c.List(ctx, &bmhs, client.InNamespace(namespace))
	return bmhs.Items, err
}

// filterNodeCondition will filter the slice of NodeConditions so that only the given conditionType remains
// and return the resulting slice.
func filterNodeCondition(conditions []corev1.NodeCondition, conditionType corev1.NodeConditionType) []corev1.NodeCondition {
	filtered := []corev1.NodeCondition{}
	for i := range conditions {
		if conditions[i].Type == conditionType {
			filtered = append(filtered, conditions[i])
		}
	}
	return filtered
}

// listBareMetalHosts logs the names, provisioning status, consumer and power status
// of all BareMetalHosts matching the opts. Similar to kubectl get baremetalhosts.
func listBareMetalHosts(ctx context.Context, c client.Client, opts ...client.ListOption) {
	bmhs := bmov1alpha1.BareMetalHostList{}
	Expect(c.List(ctx, &bmhs, opts...)).To(Succeed())
	Logf("Listing BareMetalHosts:")
	Logf("Name	Status	Consumer	Online")
	Logf("---------------------------------------------------------------------------------")
	for _, bmh := range bmhs.Items {
		consumer := ""
		if bmh.Spec.ConsumerRef != nil {
			consumer = bmh.Spec.ConsumerRef.Name
		}
		Logf("%s	%s	%s	%t", bmh.GetName(), bmh.Status.Provisioning.State, consumer, bmh.Status.PoweredOn)
	}
	Logf("---------------------------------------------------------------------------------")
	Logf("%d BareMetalHosts in total", len(bmhs.Items))
	Logf("=================================================================================")
}

// listMetal3Machines logs the names, ready status and provider ID of all Metal3Machines in the namespace.
// Similar to kubectl get metal3machines.
func listMetal3Machines(ctx context.Context, c client.Client, opts ...client.ListOption) {
	metal3Machines := infrav1.Metal3MachineList{}
	Expect(c.List(ctx, &metal3Machines, opts...)).To(Succeed())
	Logf("Listing Metal3Machines:")
	Logf("Name	Ready	Provider ID")
	Logf("---------------------------------------------------------------------------------")
	for _, metal3Machine := range metal3Machines.Items {
		providerID := ""
		if metal3Machine.Spec.ProviderID != nil {
			providerID = *metal3Machine.Spec.ProviderID
		}
		Logf("%s	%t	%s", metal3Machine.GetName(), metal3Machine.Status.Ready, providerID)
	}
	Logf("---------------------------------------------------------------------------------")
	Logf("%d Metal3Machines in total", len(metal3Machines.Items))
	Logf("=================================================================================")
}

// listMachines logs the names, status phase, provider ID and Kubernetes version
// of all Machines in the namespace. Similar to kubectl get machines.
func listMachines(ctx context.Context, c client.Client, opts ...client.ListOption) {
	machines := clusterv1.MachineList{}
	Expect(c.List(ctx, &machines, opts...)).To(Succeed())
	Logf("Listing Machines:")
	Logf("Name	Status	Provider ID	Version")
	Logf("---------------------------------------------------------------------------------")
	for _, machine := range machines.Items {
		providerID := ""
		if machine.Spec.ProviderID != nil {
			providerID = *machine.Spec.ProviderID
		}
		Logf("%s	%s	%s	%s", machine.GetName(), machine.Status.GetTypedPhase(), providerID, *machine.Spec.Version)
	}
	Logf("---------------------------------------------------------------------------------")
	Logf("%d Machines in total", len(machines.Items))
	Logf("=================================================================================")
}

// listNodes logs the names, status and Kubernetes version of all Nodes.
// Similar to kubectl get nodes.
func listNodes(ctx context.Context, c client.Client) {
	nodes := corev1.NodeList{}
	Expect(c.List(ctx, &nodes)).To(Succeed())
	Logf("Listing Nodes:")
	Logf("Name	Status	Version")
	Logf("---------------------------------------------------------------------------------")
	for _, node := range nodes.Items {
		ready := "NotReady"
		if node.Status.Conditions != nil {
			readyCondition := filterNodeCondition(node.Status.Conditions, corev1.NodeReady)
			Expect(readyCondition).To(HaveLen(1))
			if readyCondition[0].Status == corev1.ConditionTrue {
				ready = "Ready"
			}
		}
		Logf("%s	%s	%s", node.Name, ready, node.Status.NodeInfo.KubeletVersion)
	}
	Logf("---------------------------------------------------------------------------------")
	Logf("%d Nodes in total", len(nodes.Items))
	Logf("=================================================================================")
}

type waitForNumInput struct {
	Client    client.Client
	Options   []client.ListOption
	Replicas  int
	Intervals []interface{}
}

// waitForNumBmhInState will wait for the given number of BMHs to be in the given state.
func waitForNumBmhInState(ctx context.Context, state bmov1alpha1.ProvisioningState, input waitForNumInput) {
	Logf("Waiting for %d BMHs to be in %s state", input.Replicas, state)
	Eventually(func(g Gomega) {
		bmhList := bmov1alpha1.BareMetalHostList{}
		g.Expect(input.Client.List(ctx, &bmhList, input.Options...)).To(Succeed())
		g.Expect(filterBmhsByProvisioningState(bmhList.Items, state)).To(HaveLen(input.Replicas))
	}, input.Intervals...).Should(Succeed())
	listBareMetalHosts(ctx, input.Client, input.Options...)
}

// waitForNumMetal3MachinesReady will wait for the given number of M3Ms to be ready.
func waitForNumMetal3MachinesReady(ctx context.Context, input waitForNumInput) {
	Logf("Waiting for %d Metal3Machines to be ready", input.Replicas)
	Eventually(func(g Gomega) {
		m3mList := infrav1.Metal3MachineList{}
		g.Expect(input.Client.List(ctx, &m3mList, input.Options...)).To(Succeed())
		numReady := 0
		for _, m3m := range m3mList.Items {
			if m3m.Status.Ready {
				numReady++
			}
		}
		g.Expect(numReady).To(BeEquivalentTo(input.Replicas))
	}, input.Intervals...).Should(Succeed())
	listMetal3Machines(ctx, input.Client, input.Options...)
}

// waitForNumMachinesInState will wait for the given number of Machines to be in the given state.
func waitForNumMachinesInState(ctx context.Context, phase clusterv1.MachinePhase, input waitForNumInput) {
	Logf("Waiting for %d Machines to be in %s phase", input.Replicas, phase)
	inPhase := func(machine clusterv1.Machine) bool {
		return machine.Status.GetTypedPhase() == phase
	}
	waitForNumMachines(ctx, inPhase, input)
	listMachines(ctx, input.Client, input.Options...)
}

// waitForNumMachines will wait for the given number of Machines to be accepted by the accept function.
// This is a more generic function than waitForNumMachinesInState. It can be used to wait for any condition,
// e.g. that the Kubernetes version is correct.
func waitForNumMachines(ctx context.Context, accept func(clusterv1.Machine) bool, input waitForNumInput) {
	Eventually(func(g Gomega) {
		machineList := clusterv1.MachineList{}
		g.Expect(input.Client.List(ctx, &machineList, input.Options...)).To(Succeed())
		g.Expect(filterMachines(machineList.Items, accept)).To(HaveLen(input.Replicas))
	}, input.Intervals...).Should(Succeed())
	listMachines(ctx, input.Client, input.Options...)
}

// Get the machine object given its object name.
func getMachine(ctx context.Context, c client.Client, name client.ObjectKey) (result clusterv1.Machine) {
	Expect(c.Get(ctx, name, &result)).To(Succeed())
	return
}

func getMetal3Machines(ctx context.Context, c client.Client, cluster, namespace string) ([]infrav1.Metal3Machine, []infrav1.Metal3Machine) {
	var controlplane, workers []infrav1.Metal3Machine
	allMachines := &infrav1.Metal3MachineList{}
	Expect(c.List(ctx, allMachines, client.InNamespace(namespace))).To(Succeed())

	for _, machine := range allMachines.Items {
		if strings.Contains(machine.ObjectMeta.Name, "workers") {
			workers = append(workers, machine)
		} else {
			controlplane = append(controlplane, machine)
		}
	}

	return controlplane, workers
}

// metal3MachineToMachineName finds the relevant owner reference in Metal3Machine
// and returns the name of corresponding Machine.
func metal3MachineToMachineName(m3machine infrav1.Metal3Machine) (string, error) {
	ownerReferences := m3machine.GetOwnerReferences()
	for _, reference := range ownerReferences {
		if reference.Kind == "Machine" {
			return reference.Name, nil
		}
	}
	return "", fmt.Errorf("metal3machine missing a \"Machine\" kind owner reference")
}

func metal3MachineToBmhName(m3machine infrav1.Metal3Machine) string {
	return strings.Replace(m3machine.GetAnnotations()["metal3.io/BareMetalHost"], "metal3/", "", 1)
}

// Derives the name of a VM created by metal3-dev-env from the name of a BareMetalHost object.
func bmhToVMName(host bmov1alpha1.BareMetalHost) string {
	return strings.ReplaceAll(host.Name, "-", "_")
}
