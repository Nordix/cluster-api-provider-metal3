# Fake API server

Fake API server is a tool running inside a kubernetes cluster,
and generates "fake" k8s api server endpoints on demand.

## Purpose

When CAPI+CAPM3 provisions a baremetal node, after BMO and Ironic finished
provisioning the node, a node image with kubernetes components (kubelet,
containerd, etcd, etc.) is supposed to be installed inside the new node,
and as it boots up afterwards, it will start itself up as a kubernetes node.
At the end of the process, CAPI will send queries towards the newly launched
cluster's API server to verify that the cluster is fully up and running.
The address of this API server is defined in `CLUSTER_APIENDPOINT_HOST`
and `CLUSTER_APIENDPOINT_PORT` variables, which users need to provide to the
cluster template.

In a test setup with fake nodes, i.e. nodes that only "exist" in either ironic database,
(like in case of FakeIPA system), or nodes faked by BMO in its simulation mode,
the "nodes" are not able to boot up any kubernetes api server,
hence the needs of having mock API servers on-demands.

## How it works

After a BMH associating to a fake node is provisioned to `available` state,
the user can send a request towards the Fake API server endpoint `/register`,
which will spawn a new API server, with an unique IP address.

The generated API server endpoint responds to any request normally responded
by the apiserver of target clusters created in a normal CAPI workflow.

User can, then, use this IP address to feed the cluster template, and start
"provisioning" the node to a kubernetes cluster.

## How to use

You can build the `fake-api-server` image that is suitable for
your local environment with

```shell
make build-fake-api-server
```

The result is an image with label `quay.io/metal3-io/fake-apiserver:<your-arch-name>`

Alternatively, you can also build a custom image with

```shell
cd hack/fake-apiserver
docker build -t <custom tag> .
```

For local tests, it's normally needed to load the image into the cluster.
For e.g. with `minikube`

```shell
docker image save -o /tmp/api-server.tar <image-name>
minikube image load /tmp/api-server.tar
```

Now you can deploy this container to the cluster, for e.g. with a deployment

```yaml
# api-server-deployment.yaml
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: metal3-fake-api-server
  namespace: default
spec:
  replicas: 2
  selector:
    matchLabels:
      app: capim
  strategy:
    type: Recreate
  template:
    metadata:
      labels:
        app: capim
    spec:
      containers:
        - image: quay.io/metal3-io/api-server:amd64
          imagePullPolicy: IfNotPresent
          name: capim
          env:
          - name: POD_IP
            valueFrom:
              fieldRef:
                fieldPath: status.podIP
          name: apiserver
```

```shell
kubectl apply -f api-server-deployment.yaml
```

After building the container image and deploy it to a kubernetes cluster,
you need to create a tunnel to send request to it and get response, by using
a LoadBalancer, or a simple port-forward

```shell
api_server_name=$(kubectl get pods -l app=capim -o jsonpath="{.items[0].metadata.name}")
kubectl port-forward pod/${api_server_name} ${api_server_port}:3333 2>/dev/null&
```

Now, you can generate a fake API server endpoint by sending
a GET request to the fake API server. But first, let's generate some needed certificates

```shell
openssl req -x509 -subj "/CN=Kubernetes API" -new -newkey rsa:2048 \
  -nodes -keyout "/tmp/ca.key" -sha256 -days 3650 -out "/tmp/ca.crt"
openssl req -x509 -subj "/CN=ETCD CA" -new -newkey rsa:2048 \
  -nodes -keyout "/tmp/etcd.key" -sha256 -days 3650 -out "/tmp/etcd.crt"
```

```shell
caKeyEncoded=$(cat /tmp/ca.key | base64 -w 0)
caCertEncoded=$(cat /tmp/ca.crt | base64 -w 0)
etcdKeyEncoded=$(cat /tmp/etcd.key | base64 -w 0)
etcdCertEncoded=$(cat /tmp/etcd.crt | base64 -w 0)
namespace="metal3"
cluster_name="test_cluster"

cluster_endpoint=$(curl localhost:${api_server_port}/register?resource=$namespace/$cluster_name&caKey=$caKeyEncoded&caCert=$caCertEncoded&etcdKey=$etcdKeyEncoded&etcdCert=$etcdCertEncoded")
```

The fake API server will return a response with the ip and port of the newly
generated api server. These information can be fed to a CAPI infrastructure provider
(for e.g. CAPM3) to create a cluster. Notice that you need to manually create
the `ca-secret` and `etcd-secret` of the new cluster, so that it matches the certs
used by the api server.

```shell
host=$(echo ${cluster_endpoints} | jq -r ".Host")
port=$(echo ${cluster_endpoints} | jq -r ".Port")

  cat <<EOF > "/tmp/${cluster}-ca-secrets.yaml"
apiVersion: v1
kind: Secret
metadata:
  labels:
    cluster.x-k8s.io/cluster-name: ${cluster}
  name: ${cluster}-ca
  namespace: ${namespace}
type: kubernetes.io/tls
data:
  tls.crt: ${caCertEncoded}
  tls.key: ${caKeyEncoded}
EOF

  kubectl -n ${namespace} apply -f /tmp/${cluster}-ca-secrets.yaml

  cat <<EOF > "/tmp/${cluster}-etcd-secrets.yaml"
apiVersion: v1
kind: Secret
metadata:
  labels:
    cluster.x-k8s.io/cluster-name: ${cluster}
  name: ${cluster}-etcd
  namespace: ${namespace}
type: kubernetes.io/tls
data:
  tls.crt: ${etcdCertEncoded}
  tls.key: ${etcdKeyEncoded}
EOF

  kubectl -n ${namespace} apply -f /tmp/${cluster}-etcd-secrets.yaml

  # Generate metal3 cluster
  export CLUSTER_APIENDPOINT_HOST="${host}"
  export CLUSTER_APIENDPOINT_PORT="${port}"
  echo "Generating cluster ${cluster} with clusterctl"
  clusterctl generate cluster "${cluster}" \
    --from "${CLUSTER_TEMPLATE}" \
    --target-namespace "${namespace}" > /tmp/${cluster}-cluster.yaml
  kubectl apply -f /tmp/${cluster}-cluster.yaml
```

After the cluster is created, CAPI will expect that information like node name
and provider ID is registered in the API server. Since our API server doesn't
live inside the node, we will need to feed the info to it, by sending a
GET request to `/updateNode` endpoint:

```shell
curl "localhost:${api_server_port}/updateNode?resource=${namespace}/${cluster}&nodeName=${machine}&providerID=${providerID}"
```

## Acknowledgements

This was developed thanks to the implementation of
[Cluster API Provider In Memory (CAPIM)](https://github.com/kubernetes-sigs/cluster-api/tree/main/test/infrastructure/inmemory).

**NOTE:**:
This is intended for development environments only.
Do **NOT** use it in production.
