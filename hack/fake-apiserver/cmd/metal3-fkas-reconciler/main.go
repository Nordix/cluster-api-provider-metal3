package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	bmov1alpha1 "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	infrav1 "github.com/metal3-io/cluster-api-provider-metal3/api/v1beta1"

	"k8s.io/apimachinery/pkg/runtime"
	rest "k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func main() {
	// Set up logger
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	// Create a Kubernetes client
	config, err := rest.InClusterConfig()
	setupLog := ctrl.Log.WithName("setup")
	if err != nil {
		setupLog.Error(err, "Error building kubeconfig")
	}

	// Add BareMetalHost to scheme
	scheme := runtime.NewScheme()
	if err := bmov1alpha1.AddToScheme(scheme); err != nil {
		setupLog.Error(err, "Error adding BareMetalHost to scheme")
	}

	if err := infrav1.AddToScheme(scheme); err != nil {
		setupLog.Error(err, "Error adding Metal3Machine to scheme")
	}

	// Create a Kubernetes client
	mgr, err := manager.New(config, manager.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "Error creating manager")
	}

	// Set up the BareMetalHost controller
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&bmov1alpha1.BareMetalHost{}).
		Complete(reconcile.Func(func(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("Recovered from panic: %v\n", r)
					fmt.Printf("Panic occurred while reconciling BMH: %s/%s\n", req.Namespace, req.Name)
				}
			}()

			setupLog.Info("Detected change in BMH", "namespace", req.Namespace, "name", req.Name)
			bmh := &bmov1alpha1.BareMetalHost{}
			if err := mgr.GetClient().Get(ctx, req.NamespacedName, bmh); err != nil {
				fmt.Printf("Error fetching BareMetalHost: %s\n", err.Error())
				return reconcile.Result{}, err
			}

			// Check if the state has changed from "available" to "provisioning"
			if bmh.Status.Provisioning.State != "provisioning" && bmh.Status.Provisioning.State != "provisioned" {
				fmt.Printf("BMH %s/%s state is not 'provisioning' or 'provisioned'\n", req.Namespace, req.Name)
				return reconcile.Result{}, nil
			}
			uuid := bmh.ObjectMeta.UID
			if bmh.Spec.ConsumerRef == nil {
				return reconcile.Result{}, err
			}
			m3m := &infrav1.Metal3Machine{}
			m3mKey := client.ObjectKey{
				Namespace: bmh.Spec.ConsumerRef.Namespace,
				Name:      bmh.Spec.ConsumerRef.Name,
			}
			if err := mgr.GetClient().Get(ctx, m3mKey, m3m); err != nil {
				setupLog.Error(err, "Error fetching Metal3Machine", "namespace", bmh.Spec.ConsumerRef.Namespace, "name", bmh.Spec.ConsumerRef.Name)
				return reconcile.Result{}, err
			}
			labels := m3m.Labels
			nodeType := "worker"
			clusterName, ok := labels["cluster.x-k8s.io/cluster-name"]
			if !ok {
				return reconcile.Result{}, err
			}
			if _, ok := labels["cluster.x-k8s.io/control-plane"]; ok {
				nodeType = "control-plane"
			}
			providerID := fmt.Sprintf("metal3://%s/%s/%s", m3m.Namespace, bmh.Name, m3m.Name)
			url := "http://localhost:3333/updateNode"
			requestData := map[string]string{
				"resource":   fmt.Sprintf("%s/%s", m3m.Namespace, clusterName),
				"nodeName":   m3m.Name,
				"providerID": providerID,
				"uuid":       string(uuid),
				"nodeType":   nodeType,
			}
			jsonData, err := json.Marshal(requestData)
			if err != nil {
				fmt.Printf("Error marshalling JSON: %s", err.Error())
				return reconcile.Result{}, err
			}
			setupLog.Info("Making POST request", "content", string(jsonData))
			resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
			if err != nil {
				fmt.Printf("Error making POST request: %s", err.Error())
				return reconcile.Result{}, err
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				fmt.Printf("POST request failed with status: %s", resp.Status)
				return reconcile.Result{}, fmt.Errorf("POST request failed with status: %s", resp.Status)
			}

			return reconcile.Result{}, nil
		})); err != nil {
		setupLog.Error(err, "Error setting up controller")
	}

	// Start the manager
	setupLog.Info("Starting controller...")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Error starting manager")
	}
}
