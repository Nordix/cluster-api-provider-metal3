#!/bin/bash

set -euxo pipefail

REPO_ROOT=$(realpath "$(dirname "$(realpath "${BASH_SOURCE[0]}")")"/..)
cd "${REPO_ROOT}"
export CAPM3PATH="${REPO_ROOT}"

export CAPM3RELEASEBRANCH="${CAPM3RELEASEBRANCH:-main}"
export IPAMRELEASEBRANCH="${IPAMRELEASEBRANCH:-main}"

# Extract release version from release-branch name
if [[ "${CAPM3RELEASEBRANCH}" == release-* ]]; then
    CAPM3_RELEASE_PREFIX="${CAPM3RELEASEBRANCH#release-}"
    export CAPM3RELEASE="v${CAPM3_RELEASE_PREFIX}.99"
    export IPAMRELEASE="v${CAPM3_RELEASE_PREFIX}.99"
    export CAPI_RELEASE_PREFIX="v${CAPM3_RELEASE_PREFIX}."
else
    export CAPM3RELEASE="v1.14.99"
    export IPAMRELEASE="v1.14.99"
    export CAPI_RELEASE_PREFIX="v1.13."
fi

# Default CAPI_CONFIG_FOLDER to $HOME/.config folder if XDG_CONFIG_HOME not set
CONFIG_FOLDER="${XDG_CONFIG_HOME:-$HOME/.config}"
export CAPI_CONFIG_FOLDER="${CONFIG_FOLDER}/cluster-api"

# shellcheck source=./scripts/environment.sh
source "${REPO_ROOT}/scripts/environment.sh"

# Force docker as the container runtime
export CONTAINER_RUNTIME="docker"

# Always set DATE variable for nightly builds because it is needed to form
# the URL for CAPI nightly build components in e2e_conf.yaml even if not used.
DATE=$(date '+%Y%m%d' -d '1 day ago')
export DATE

# If CAPI_NIGHTLY_BUILD is true, it means that the tests are run against the
# nightly build of CAPI components which are built from CAPI's main branch.
if [[ "${CAPI_NIGHTLY_BUILD:-false}" == "true" ]]; then
  export CAPIRELEASE="v1.14.99"
fi

mkdir -p "${CAPI_CONFIG_FOLDER}"

# Start fresh clusterctl.yaml each run
: > "${CAPI_CONFIG_FOLDER}/clusterctl.yaml"

case "${GINKGO_FOCUS:-}" in
  features)
    echo "ENABLE_BMH_NAME_BASED_PREALLOCATION: true" >>"${CAPI_CONFIG_FOLDER}/clusterctl.yaml"
  ;;

  scalability)
    echo 'CLUSTER_TOPOLOGY: true' >>"${CAPI_CONFIG_FOLDER}/clusterctl.yaml"
    # Build FKAS image from source so that the scalability test uses the latest code
    FKAS_TAG=ci make docker-build-fkas
  ;;

  in-place-upgrade)
    # Enable Cluster Topology and in-place updates features
    echo 'CLUSTER_TOPOLOGY: true' >>"${CAPI_CONFIG_FOLDER}/clusterctl.yaml"
    echo 'EXP_RUNTIME_SDK: true' >>"${CAPI_CONFIG_FOLDER}/clusterctl.yaml"
    echo 'EXP_IN_PLACE_UPDATES: true' >>"${CAPI_CONFIG_FOLDER}/clusterctl.yaml"
  ;;
esac

echo 'EXP_MACHINE_TAINT_PROPAGATION: true' >> "${CAPI_CONFIG_FOLDER}/clusterctl.yaml"

if [[ ${GINKGO_FOCUS:-} != "scalability" ]]; then
  # Don't run scalability tests if not asked for.
    export GINKGO_SKIP="${GINKGO_SKIP:-} scalability"
fi

# Ensure required tools are available.
# Install Go first if not already present
export GO_VERSION="${GO_VERSION:-1.26.4}"
# shellcheck source=./hack/install-go.sh
source "${REPO_ROOT}/hack/install-go.sh"
hash -r

# Verify Go installation
# shellcheck source=./hack/ensure-go.sh
source "${REPO_ROOT}/hack/ensure-go.sh"
PATH=$PATH:$(go env GOPATH)/bin
# shellcheck source=./hack/ensure-kind.sh
source "${REPO_ROOT}/hack/ensure-kind.sh"
# shellcheck source=./hack/ensure-kubectl.sh
source "${REPO_ROOT}/hack/ensure-kubectl.sh"
# shellcheck source=./hack/ensure-docker.sh
source "${REPO_ROOT}/hack/ensure-docker.sh"
if [[ -S /var/run/docker.sock ]]; then
  sudo chown "${USER}":"$(id -gn)" /var/run/docker.sock
  sudo chmod 600 /var/run/docker.sock
fi

# If running in-place-upgrade tests, ensure extension namespace and ssh key secret exist
if [[ "${GINKGO_FOCUS:-}" == "in-place-upgrade" ]]; then
  EXT_NS="test-extension-system"
  kubectl get ns "${EXT_NS}" >/dev/null 2>&1 || kubectl create ns "${EXT_NS}"
  # Recreate the secret to ensure freshest key is used
  kubectl -n "${EXT_NS}" delete secret ssh-key >/dev/null 2>&1 || true
  kubectl -n "${EXT_NS}" create secret generic ssh-key \
    --from-file=id_rsa="${HOME}/.ssh/id_rsa"
fi

# Ensure kustomize + envsubst (tooling used below)
make kustomize envsubst
export PATH="${REPO_ROOT}/hack/tools/bin:${PATH}"

## --- Ironic credentials and TLS setup ---
## These were previously sourced from metal3-dev-env libs.
## Now managed directly here.

# Helper to generate a random UUID-like string for credentials.
uuid_gen() {
  cat /proc/sys/kernel/random/uuid 2>/dev/null || python3 -c "import uuid; print(uuid.uuid4())"
}

export IRONIC_DATA_DIR="${IRONIC_DATA_DIR:-/opt/metal3/ironic}"
export IRONIC_AUTH_DIR="${IRONIC_AUTH_DIR:-${IRONIC_DATA_DIR}/auth}"

sudo mkdir -p "${IRONIC_DATA_DIR}"
sudo chown -R "${USER}:$(id -gn)" "${IRONIC_DATA_DIR}"

export IRONIC_NAMESPACE="${IRONIC_NAMESPACE:-baremetal-operator-system}"
export IRONIC_BASIC_AUTH="${IRONIC_BASIC_AUTH:-true}"
export IRONIC_TLS_SETUP="${IRONIC_TLS_SETUP:-true}"
export IRONIC_KEEPALIVED="${IRONIC_KEEPALIVED:-true}"
export IRONIC_USE_MARIADB="${IRONIC_USE_MARIADB:-false}"
export REGISTRY="${REGISTRY:-172.22.0.1:5000}"
if [[ "${CAPM3RELEASEBRANCH}" == "main" ]]; then
  export BARE_METAL_OPERATOR_IMAGE="${BARE_METAL_OPERATOR_IMAGE:-quay.io/metal3-io/baremetal-operator:main}"
  export IRONIC_IMAGE="${IRONIC_IMAGE:-quay.io/metal3-io/ironic:main}"
  export IPA_DOWNLOADER_IMAGE="${IPA_DOWNLOADER_IMAGE:-registry.nordix.org/quay-io-proxy/metal3-io/ironic-ipa-downloader:latest}"
  export IRONIC_KEEPALIVED_IMAGE="${IRONIC_KEEPALIVED_IMAGE:-registry.nordix.org/quay-io-proxy/metal3-io/keepalived:latest}"
  export IPA_BRANCH="${IPA_BRANCH:-master}"
  export IRSO_IRONIC_VERSION="${IRSO_IRONIC_VERSION:-latest}"
else
  # For future releases, set versions according to the compatibility matrix:
  # https://book.metal3.io/version_support.html
  :
fi

# Provisioning network vars (previously set by metal3-dev-env)
export CLUSTER_PROVISIONING_IP="${CLUSTER_PROVISIONING_IP:-172.22.0.2}"
export CLUSTER_BARE_METAL_PROVISIONER_IP="${CLUSTER_BARE_METAL_PROVISIONER_IP:-172.22.0.2}"
export CLUSTER_DHCP_RANGE_START="${CLUSTER_DHCP_RANGE_START:-172.22.0.10}"
export CLUSTER_DHCP_RANGE_END="${CLUSTER_DHCP_RANGE_END:-172.22.0.100}"
export BARE_METAL_PROVISIONER_NETWORK="${BARE_METAL_PROVISIONER_NETWORK:-172.22.0.0/24}"
export BARE_METAL_PROVISIONER_CIDR="${BARE_METAL_PROVISIONER_CIDR:-24}"
export BARE_METAL_PROVISIONER_INTERFACE="${BARE_METAL_PROVISIONER_INTERFACE:-ironicendpoint}"

update_kustomize_image() {
  local image_name="$1"
  local env_var_name="$2"
  local kustomize_dir="$3"
  local full_image="${!env_var_name}"

  if [[ -z "${full_image}" ]]; then
    echo "Environment variable ${env_var_name} is not set."
    return 1
  fi

  if [[ ! -f "${kustomize_dir}/kustomization.yaml" ]]; then
    echo "No kustomization.yaml found in ${kustomize_dir}"
    return 1
  fi

  echo "Updating image for ${image_name} to ${full_image} in ${kustomize_dir}/kustomization.yaml"
  (cd "${kustomize_dir}" && kustomize edit set image "${image_name}=${full_image}")
}

yaml_envsubst() {
  local dir="$1"
  for file in "${dir}"/*.yaml; do
    if [[ -f "${file}" ]]; then
      local tmp_file
      tmp_file=$(mktemp)
      envsubst < "${file}" > "${tmp_file}" && mv "${tmp_file}" "${file}"
    fi
  done
}

BMO_OVERLAYS=(
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/release-0.12"
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/release-0.13"
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/pr-test"
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/release-main"
)
IRSO_IRONIC_OVERLAYS=(
  "${REPO_ROOT}/test/e2e/data/ironic-standalone-operator/ironic/overlays/release-33.0"
  "${REPO_ROOT}/test/e2e/data/ironic-standalone-operator/ironic/overlays/release-35.0"
  "${REPO_ROOT}/test/e2e/data/ironic-standalone-operator/ironic/overlays/pr-test"
  "${REPO_ROOT}/test/e2e/data/ironic-standalone-operator/ironic/overlays/main"
)
IRSO_OPERATOR_OVERLAYS=(
  "${REPO_ROOT}/test/e2e/data/ironic-standalone-operator/operator/overlays/release-0.8.0"
  "${REPO_ROOT}/test/e2e/data/ironic-standalone-operator/operator/overlays/release-0.9.0"
)

# Update BMO image in overlays
case "${REPO_NAME:-}" in
  baremetal-operator)
    export BARE_METAL_OPERATOR_IMAGE="${REGISTRY}/localimages/tested_repo:latest"
    ;;
  ironic-image)
    export IRONIC_IMAGE="${REGISTRY}/localimages/tested_repo:latest"
    ;;
esac

update_kustomize_image quay.io/metal3-io/baremetal-operator BARE_METAL_OPERATOR_IMAGE "${REPO_ROOT}"/test/e2e/data/bmo-deployment/overlays/pr-test

# Apply envsubst to kustomization.yaml files in BMO and Ironic overlays
yaml_envsubst "${REPO_ROOT}"/test/e2e/data/bmo-deployment/overlays/pr-test
yaml_envsubst "${REPO_ROOT}"/test/e2e/data/ironic-standalone-operator/ironic/base/
yaml_envsubst "${REPO_ROOT}"/test/e2e/data/ironic-standalone-operator/ironic/components/basic-auth/
yaml_envsubst "${REPO_ROOT}"/test/e2e/data/ironic-standalone-operator/ironic/components/tls/
yaml_envsubst "${REPO_ROOT}"/test/e2e/data/ironic-standalone-operator/operator/components/configmap/

for overlay in "${IRSO_IRONIC_OVERLAYS[@]}"; do
  yaml_envsubst "${overlay}"
done

for overlay in "${IRSO_OPERATOR_OVERLAYS[@]}"; do
  yaml_envsubst "${overlay}"
done

# Generate Ironic basic auth credentials
if [[ "${IRONIC_BASIC_AUTH}" == "true" ]]; then
  mkdir -p "${IRONIC_AUTH_DIR}"

  if [[ -z "${IRONIC_USERNAME:-}" ]]; then
    if [[ ! -f "${IRONIC_AUTH_DIR}/ironic-username" ]]; then
      IRONIC_USERNAME="$(uuid_gen)"
      echo "${IRONIC_USERNAME}" > "${IRONIC_AUTH_DIR}/ironic-username"
    else
      IRONIC_USERNAME="$(cat "${IRONIC_AUTH_DIR}/ironic-username")"
    fi
  fi
  if [[ -z "${IRONIC_PASSWORD:-}" ]]; then
    if [[ ! -f "${IRONIC_AUTH_DIR}/ironic-password" ]]; then
      IRONIC_PASSWORD="$(uuid_gen)"
      echo "${IRONIC_PASSWORD}" > "${IRONIC_AUTH_DIR}/ironic-password"
    else
      IRONIC_PASSWORD="$(cat "${IRONIC_AUTH_DIR}/ironic-password")"
    fi
  fi

  export IRONIC_USERNAME
  export IRONIC_PASSWORD

  echo "${IRONIC_USERNAME}" > "${REPO_ROOT}"/test/e2e/data/ironic-standalone-operator/ironic/components/basic-auth/ironic-username
  echo "${IRONIC_PASSWORD}" > "${REPO_ROOT}"/test/e2e/data/ironic-standalone-operator/ironic/components/basic-auth/ironic-password
fi

# Generate TLS certificates for Ironic if TLS is enabled
if [[ "${IRONIC_TLS_SETUP}" == "true" ]]; then
  IRONIC_TLS_DIR="${IRONIC_DATA_DIR}/tls"
  mkdir -p "${IRONIC_TLS_DIR}"

  IRONIC_KEY_FILE="${IRONIC_KEY_FILE:-${IRONIC_TLS_DIR}/ironic-key.pem}"
  IRONIC_CERT_FILE="${IRONIC_CERT_FILE:-${IRONIC_TLS_DIR}/ironic-cert.pem}"

  if [[ ! -f "${IRONIC_CERT_FILE}" ]]; then
    openssl req -x509 -newkey rsa:4096 \
      -keyout "${IRONIC_KEY_FILE}" \
      -out "${IRONIC_CERT_FILE}" \
      -days 365 -nodes \
      -subj "/CN=ironic" \
      -addext "subjectAltName=IP:${PROVISIONING_IP:-172.22.0.1},IP:${EXTERNAL_SUBNET_V4_HOST:-192.168.111.1}"
  fi

  cp "${IRONIC_KEY_FILE}" "${REPO_ROOT}"/test/e2e/data/ironic-standalone-operator/ironic/components/tls/
  cp "${IRONIC_CERT_FILE}" "${REPO_ROOT}"/test/e2e/data/ironic-standalone-operator/ironic/components/tls/
fi

for overlay in "${BMO_OVERLAYS[@]}"; do
  if [[ "${IRONIC_BASIC_AUTH}" == "true" ]]; then
    echo "${IRONIC_USERNAME}" > "${overlay}/ironic-username"
    echo "${IRONIC_PASSWORD}" > "${overlay}/ironic-password"
  fi
done

## --- Virtual bare metal lab setup via vbmctl ---

export PROVISIONING_IP="${PROVISIONING_IP:-172.22.0.1}"
export EXTERNAL_SUBNET_V4_HOST="${EXTERNAL_SUBNET_V4_HOST:-192.168.111.1}"
export CLUSTER_EXTERNAL_IP="${CLUSTER_EXTERNAL_IP:-192.168.111.2}"
export PROVISIONING_BRIDGE="${PROVISIONING_BRIDGE:-metal3}"
export EXTERNAL_BRIDGE="${EXTERNAL_BRIDGE:-external}"
export PROVISIONING_NETWORK_NAME="${PROVISIONING_NETWORK_NAME:-provisioning-e2e}"
export EXTERNAL_NETWORK_NAME="${EXTERNAL_NETWORK_NAME:-external-e2e}"
export BMC_EMULATOR_PORT="${BMC_EMULATOR_PORT:-8000}"
export BMC_EMULATOR_IMAGE="${BMC_EMULATOR_IMAGE:-quay.io/metal3-io/sushy-tools:latest}"

# TODO: Remove once vbmctl ships as a BMO release artifact; replace with a
# direct download. See hack/hack-build-vbmctl.sh.
# shellcheck source=./hack/build-vbmctl.sh
source "${REPO_ROOT}/hack/build-vbmctl.sh"

if ! [[ "${NUM_NODES}" =~ ^[0-9]+$ ]] || [[ "${NUM_NODES}" -lt 1 ]]; then
  echo "NUM_NODES must be a positive integer, got: ${NUM_NODES}" >&2
  exit 1
fi

generate_vbmctl_vms() {
  local vm_entries=""
  local i

  for ((i=0; i<NUM_NODES; i++)); do
    local suffix
    suffix=$(printf "%02d" "$((i + 1))")
    vm_entries+="  - name: node-${i}
    memory: 4096
    vcpus: 2
    volumes:
    - name: \"1\"
      size: 20
    networkAttachments:
    - network: ${PROVISIONING_NETWORK_NAME}
      macAddress: \"00:60:2f:31:81:${suffix}\"
    - network: ${EXTERNAL_NETWORK_NAME}
      macAddress: \"00:60:2f:32:81:${suffix}\"
"
  done

  export VBMCTL_VMS="${vm_entries%$'\n'}"
}

# Generate bmcs config dynamically based on NUM_NODES
generate_bmcs_config() {
  local bmcs_entries=""
  local i

  for ((i=0; i<NUM_NODES; i++)); do
    local suffix
    suffix=$(printf "%02d" "$((i + 1))")
    local ip_last_octet=$((20 + i))
    bmcs_entries+="- name: \"node-${i}\"
  address: \"redfish-virtualmedia+http://${PROVISIONING_IP}:${BMC_EMULATOR_PORT}/redfish/v1/Systems/node-${i}\"
  bootMacAddress: \"00:60:2f:31:81:${suffix}\"
  ipAddress: \"192.168.111.${ip_last_octet}\"
  user: admin
  password: password
  rootDeviceHints:
    deviceName: \"/dev/vda\"
"
  done

  echo -n "${bmcs_entries}" > "${E2E_BMCS_CONFIG}"
}

# Generate vbmctl config from template
VBMCTL_CONFIG="${REPO_ROOT}/_out/vbmctl.yaml"
generate_vbmctl_vms
envsubst < "${REPO_ROOT}/test/e2e/config/vbmctl.yaml.tmpl" > "${VBMCTL_CONFIG}"

# Generate bmcs config dynamically (consumed by Go tests to create BMH objects)
export E2E_BMCS_CONFIG="${REPO_ROOT}/_out/bmcs.yaml"
generate_bmcs_config


# Ensure Ironic data directories exist before vbmctl creates the lab
mkdir -p "${IRONIC_DATA_DIR}/html/images"

# Create virtual bare metal lab (VMs, networks, BMC emulator, image server)
echo "Creating virtual bare metal lab with vbmctl..."
"${VBMCTL}" -c "${VBMCTL_CONFIG}" create bml

# Workaround: sushy-tools may start before the bridge IP is assigned,
# causing "Cannot assign requested address". Restart to pick up the IP.
if docker inspect vbmctl-sushy-tools &>/dev/null && \
   ! curl -sf "http://${PROVISIONING_IP}:${BMC_EMULATOR_PORT}/redfish/v1/" &>/dev/null; then
  echo "Restarting sushy-tools (BMC emulator bind race workaround)..."
  docker restart vbmctl-sushy-tools
  sleep 2
fi

# Docker's nftables FORWARD chain uses "policy drop" and only accepts traffic
# on its own bridges (docker0, kind-bridge). Libvirt sets up NAT and forwarding
# rules in a separate table (libvirt_network) but packets must also pass
# Docker's filter chain. Add accept rules to Docker's DOCKER-USER chain
# (the standard hook for user-defined overrides) for the libvirt bridges.
echo "Adding nftables rules for VM internet access..."
add_nft_rule() {
  # Only add the rule if it doesn't already exist (makes the script idempotent).
  if ! sudo nft list chain ip filter DOCKER-USER 2>/dev/null | grep -Fq -- "$*"; then
    sudo nft add rule ip filter DOCKER-USER "$@"
  fi
}
add_nft_rule iifname "${EXTERNAL_BRIDGE}" accept
add_nft_rule oifname "${EXTERNAL_BRIDGE}" accept
add_nft_rule iifname "${PROVISIONING_BRIDGE}" accept
add_nft_rule oifname "${PROVISIONING_BRIDGE}" accept

# Name of the kind management cluster created by the Go test framework.
# Must match managementClusterName in test/e2e/config/e2e_conf.yaml.
export MANAGEMENT_CLUSTER_NAME="${MANAGEMENT_CLUSTER_NAME:-capm3-e2e}"

# Cleanup function
cleanup() {
  echo "Cleaning up virtual bare metal lab..."
  "${VBMCTL}" -c "${VBMCTL_CONFIG}" delete bml || true

  # The kind management cluster is created by the Go test framework. When a run
  # is interrupted or fails before the framework's own teardown, the cluster is
  # left behind and blocks the next run ("node(s) already exist for a cluster").
  # Delete it here too, unless the caller asked to keep resources for debugging.
  if [[ "${SKIP_CLEANUP:-false}" != "true" ]]; then
    echo "Deleting kind management cluster '${MANAGEMENT_CLUSTER_NAME}'..."
    kind delete cluster --name "${MANAGEMENT_CLUSTER_NAME}" || true
  fi
}
trap cleanup EXIT

# Delete any leftover kind management cluster from a previous interrupted run so
# the framework can create a fresh one (makes the script idempotent/re-runnable).
echo "Removing any pre-existing kind cluster '${MANAGEMENT_CLUSTER_NAME}'..."
kind delete cluster --name "${MANAGEMENT_CLUSTER_NAME}" || true

# Run e2e tests
# The bootstrap cluster (kind) is created by the Go test framework
# via CAPI's bootstrap package. VMs are already running from vbmctl above.
export E2E_COPY_KUBECONFIG=true
if [[ -n "${CLUSTER_TOPOLOGY:-}" ]]; then
  export CLUSTER_TOPOLOGY=true
  make e2e-clusterclass-tests
else
  export EXP_MACHINE_TAINT_PROPAGATION=true
  make e2e-tests
fi
