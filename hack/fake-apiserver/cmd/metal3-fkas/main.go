package main

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/api/v1alpha1"
	cmanager "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/runtime/manager"
	"sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"
	"sigs.k8s.io/cluster-api/util/certs"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	cloudScheme  = runtime.NewScheme()
	setupLog     = ctrl.Log.WithName("setup")
	scheme       = runtime.NewScheme()
	cloudMgr     = cmanager.New(cloudScheme)
	apiServerMux = &server.WorkloadClustersMux{}
	ctx          = context.Background()
)

func init() {
	// scheme used for operating on the management cluster.
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterv1.AddToScheme(scheme)
	_ = infrav1.AddToScheme(scheme)

	// scheme used for operating on the cloud resource.
	_ = corev1.AddToScheme(cloudScheme)
	_ = appsv1.AddToScheme(cloudScheme)
	_ = rbacv1.AddToScheme(cloudScheme)
}

type ResourceData struct {
	ResourceName string
	Host         string
	Port         int
}

// register receives a resourceName, ca and etcd secrets (key+cert) from a request
// and generates a fake k8s API server corresponding with the provided name and secrets.
func register(w http.ResponseWriter, r *http.Request) {
	var requestData struct {
		ResourceName string `json:"resource"`
		CaKey        string `json:"caKey"`
		CaCert       string `json:"caCert"`
		EtcdKey      string `json:"etcdKey"`
		EtcdCert     string `json:"etcdCert"`
	}

	if err := json.NewDecoder(r.Body).Decode(&requestData); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		setupLog.Error(err, "Invalid JSON in request body")
		return
	}

	setupLog.Info("Registering new resource", "resource", requestData.ResourceName)
	resourceName := requestData.ResourceName
	resp := &ResourceData{}
	resp.ResourceName = resourceName
	setupLog.Info("Adding resource group", "resourceName", resourceName)
	cloudMgr.AddResourceGroup(resourceName)
	// NOTE: We are using resourceName as listener name for convenience
	listener, err := apiServerMux.InitWorkloadClusterListener(resourceName)
	if err != nil {
		setupLog.Error(err, "Failed to initialize listener", "resourceName", resourceName)
		http.Error(w, "Failed to initialize listener", http.StatusInternalServerError)
		setupLog.Error(err, "failed to Initialize listener")
		return
	}
	// NOTE: The two params are name of the listener and name of the resource group
	// we are setting both of them to resourceName for convenience, but it is not required.
	if err := apiServerMux.RegisterResourceGroup(resourceName, resourceName); err != nil {
		setupLog.Error(err, "Failed to register resource group to listener", "resourceName", resourceName)
		http.Error(w, "Failed to register resource group to listener", http.StatusInternalServerError)
		setupLog.Error(err, "failed to Register resource group to listener")
		return
	}
	caKeyEncoded := requestData.CaKey
	caKeyRaw, err := base64.StdEncoding.DecodeString(caKeyEncoded)
	if err != nil {
		setupLog.Error(err, "Failed to generate caKey", "resourceName", resourceName)
		http.Error(w, "Failed to generate caKey", http.StatusInternalServerError)
		setupLog.Error(err, "failed to generate caKey")
		return
	}
	caCertEncoded := requestData.CaCert
	caCertRaw, err := base64.StdEncoding.DecodeString(caCertEncoded)
	if err != nil {
		setupLog.Error(err, "Failed to generate caCert", "resourceName", resourceName)
		http.Error(w, "Failed to generate caCert", http.StatusInternalServerError)
		setupLog.Error(err, "failed to generate caKey")
		return
	}

	caCert, err := certs.DecodeCertPEM(caCertRaw)
	if err != nil {
		setupLog.Error(err, "Failed to add API server", "resourceName", resourceName)
		http.Error(w, "Failed to add API server", http.StatusInternalServerError)
		setupLog.Error(err, "failed to generate caKey")
		return
	}
	caKey, err := certs.DecodePrivateKeyPEM(caKeyRaw)
	if err != nil {
		setupLog.Error(err, "Failed to generate etcdKey", "resourceName", resourceName)
		http.Error(w, "Failed to generate etcdKey", http.StatusInternalServerError)
		setupLog.Error(err, "failed to generate caKey")
		return
	}

	apiServerPod1 := "kube-apiserver-1"
	err = apiServerMux.AddAPIServer(resourceName, apiServerPod1, caCert, caKey.(*rsa.PrivateKey))
	if err != nil {
		setupLog.Error(err, "Failed to generate etcdCert", "resourceName", resourceName)
		http.Error(w, "Failed to generate etcdCert", http.StatusInternalServerError)
		setupLog.Error(err, "failed to add API server")
		return
	}
	etcdKeyEncoded := requestData.EtcdKey
	etcdKeyRaw, err := base64.StdEncoding.DecodeString(etcdKeyEncoded)
	if err != nil {
		setupLog.Error(err, "Failed to add etcd member", "resourceName", resourceName)
		http.Error(w, "Failed to add etcd member", http.StatusInternalServerError)
		setupLog.Error(err, "failed to generate etcdKey")
		return
	}

	etcdCertEncoded := requestData.EtcdCert
	etcdCertRaw, err := base64.StdEncoding.DecodeString(etcdCertEncoded)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		setupLog.Error(err, "failed to generate etcdKey")
		return
	}
	//
	etcdCert, err := certs.DecodeCertPEM(etcdCertRaw)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		setupLog.Error(err, "failed to generate etcdKey")
		return
	}
	etcdKey, err := certs.DecodePrivateKeyPEM(etcdKeyRaw)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		setupLog.Error(err, "failed to generate etcdKey")
		return
	}
	if etcdKey == nil {
		http.Error(w, "", http.StatusInternalServerError)
		setupLog.Error(err, "failed to generate etcdKey")
		return
	}
	//
	etcdPodMember1 := "etcd-1"
	err = apiServerMux.AddEtcdMember(resourceName, etcdPodMember1, etcdCert, etcdKey.(*rsa.PrivateKey))
	if err != nil {
		setupLog.Error(err, "failed to add etcd member")
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	resp.Host = listener.Host()
	resp.Port = listener.Port()
	data, err := json.Marshal(resp)
	if err != nil {
		setupLog.Error(err, "Failed to marshal the response", "resourceName", resourceName)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	c, err := listener.GetClient()
	if err != nil {
		setupLog.Error(err, "Failed to get listener client", "resourceName", resourceName)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	ctx := context.Background()

	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kubeadm:get-nodes",
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"get"},
				APIGroups: []string{"core"},
				Resources: []string{"nodes"},
			},
		},
	}
	if err = c.Create(ctx, role); err != nil {
		setupLog.Error(err, "Failed to create cluster role", "resourceName", resourceName)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	roleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kubeadm:get-nodes",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "kubeadm:get-nodes",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: rbacv1.GroupKind,
				Name: "system:bootstrappers:kubeadm:default-node-token",
			},
		},
	}
	if err = c.Create(ctx, roleBinding); err != nil {
		setupLog.Error(err, "Failed to create cluster rolebinding", "resourceName", resourceName)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	// create kubeadm config map
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubeadm-config",
			Namespace: metav1.NamespaceSystem,
		},
		Data: map[string]string{
			"ClusterConfiguration": "",
		},
	}
	if err = c.Create(ctx, cm); err != nil {
		setupLog.Error(err, "Failed to create kubeadm configmap", "resourceName", resourceName)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err = w.Write(data); err != nil {
		setupLog.Error(err, "failed to write the response data")
		return
	}
}

// updateNode receives nodeName and providerID, which are provided by CAPI after
// node provisioning, from request, and update the Node object on the fake API server accordingly.
func updateNode(w http.ResponseWriter, r *http.Request) {
	var requestData struct {
		ResourceName string `json:"resource"`
		UUID         string `json:"uuid"`
		NodeName     string `json:"nodeName"`
		ProviderID   string `json:"providerID"`
		NodeType     string `json:"nodeType"`
	}

	if err := json.NewDecoder(r.Body).Decode(&requestData); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		setupLog.Error(err, "Invalid JSON in request body")
		return
	}

	listener := cloudMgr.GetResourceGroup(requestData.ResourceName)
	timeOutput := metav1.Now()
	setupLog.Info("Updating node", "resource: ", requestData.ResourceName, "nodeName: ", requestData.NodeName, "providerID:", requestData.ProviderID)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: requestData.NodeName,
			Labels: map[string]string{
				"metal3.io/uuid": requestData.UUID,
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: requestData.ProviderID,
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					LastHeartbeatTime:  timeOutput,
					LastTransitionTime: timeOutput,
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
				},
				{
					LastHeartbeatTime:  timeOutput,
					LastTransitionTime: timeOutput,
					Type:               corev1.NodeMemoryPressure,
					Status:             corev1.ConditionFalse,
					Message:            "kubelet has sufficient memory available",
					Reason:             "KubeletHasSufficientMemory",
				},
				{
					LastHeartbeatTime:  timeOutput,
					LastTransitionTime: timeOutput,
					Message:            "kubelet has no disk pressure",
					Reason:             "KubeletHasNoDiskPressure",
					Status:             corev1.ConditionFalse,
					Type:               corev1.NodeDiskPressure,
				},
				{
					LastHeartbeatTime:  timeOutput,
					LastTransitionTime: timeOutput,
					Message:            "kubelet has sufficient PID available",
					Reason:             "KubeletHasSufficientPID",
					Status:             corev1.ConditionFalse,
					Type:               corev1.NodePIDPressure,
				},
				{
					LastHeartbeatTime:  timeOutput,
					LastTransitionTime: timeOutput,
					Message:            "kubelet is posting ready status",
					Reason:             "KubeletReady",
					Status:             corev1.ConditionTrue,
					Type:               corev1.NodeReady,
				},
			},
			NodeInfo: corev1.NodeSystemInfo{
				Architecture:    "amd64",
				BootID:          "a4254236-e1e3-4462-97ed-4a25b8b29884",
				OperatingSystem: "linux",
				SystemUUID:      "1ce97e94-730c-42b7-98da-f7dcc0b58e93",
			},
		},
	}

	if requestData.NodeType == "control-plane" {
		node.Labels["node-role.kubernetes.io/control-plane"] = ""
	}
	c := listener.GetClient()
	err := c.Create(ctx, node)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			setupLog.Info("Node already exists", "nodeName", requestData.NodeName)
			w.WriteHeader(http.StatusOK)
			return
		}
		logLine := fmt.Sprintf("Error adding node %s as type %s. Error: %s", requestData.NodeName, requestData.NodeType, err)
		setupLog.Error(err, "Error adding node", "nodeName", requestData.NodeName, "nodeType", requestData.NodeType)
		http.Error(w, logLine, http.StatusInternalServerError)
		return
	}
}

func main() {
	log.SetLogger(zap.New(zap.UseDevMode(true)))
	podIP := os.Getenv("POD_IP")
	apiServerMux, _ = server.NewWorkloadClustersMux(cloudMgr, podIP)
	setupLog.Info("Starting the FKAS server")
	http.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		setupLog.Info("Received request to /register", "content", r.Body)
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		register(w, r)
	})
	http.HandleFunc("/updateNode", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		updateNode(w, r)
	})
	server := &http.Server{
		Addr:         ":3333",
		Handler:      nil,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  15 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		setupLog.Error(err, "Error starting server")
		os.Exit(1)
	}
}
