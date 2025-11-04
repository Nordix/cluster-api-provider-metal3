# -*- mode: Python -*-
# Minimal Tiltfile for cluster-api-provider-metal3

# Anonymous, ephemeral image registry.
default_registry('ttl.sh')

# Allow Tilt to use the local Docker daemon
allow_k8s_contexts('kubernetes-admin@kubernetes')

# versions of dependencies
settings = {
    "capi_version": "v1.11.2",
}

local_resource(
    "deploy-bmo",
    cmd='''
kustomize build https://github.com/Nordix/baremetal-operator//config/default?ref=devel/bmo-default-kustomize | kubectl apply -f -
kubectl wait --for=condition=Available --timeout=300s -n baremetal-operator-system deployment/baremetal-operator-controller-manager
    ''',
    resource_deps=["deploy-cert-manager"],
    labels=["dependencies"],
)

# Deploy IPAM as a Tilt resource
local_resource(
    "deploy-ipam",
    cmd='curl -Ls https://github.com/metal3-io/ip-address-manager/releases/latest/download/ipam-components.yaml | kubectl apply -f -',
    resource_deps=["deploy-cert-manager"],
    labels=["dependencies"],
)

local_resource(
    "build-manager",
    cmd='CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -extldflags=-static" -o manager',
    deps=["main.go", "api", "controllers", "pkg", "go.mod", "go.sum"],
    resource_deps=["deploy-capi", "deploy-bmo", "deploy-ipam"],
    labels=["bins"],
