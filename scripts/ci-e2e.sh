#!/usr/bin/env bash

set -euxo pipefail

REPO_ROOT=$(realpath "$(dirname "$(realpath "${BASH_SOURCE[0]}")")"/..)
cd "${REPO_ROOT}" || exit 1
export CAPM3PATH="${REPO_ROOT}"
export WORKING_DIR=/tmp/metal3-dev-env
FORCE_REPO_UPDATE="${FORCE_REPO_UPDATE:-false}"

export CAPM3RELEASEBRANCH="${CAPM3RELEASEBRANCH:-main}"
export IPAMRELEASEBRANCH="${IPAMRELEASEBRANCH:-main}"

# Verify they are available and have correct versions.
PATH=$PATH:/usr/local/go/bin
PATH=$PATH:$(go env GOPATH)/bin

"${REPO_ROOT}/hack/ensure-go.sh"
# shellcheck source=./hack/ensure-kind.sh
source "${REPO_ROOT}/hack/ensure-kind.sh"
# shellcheck source=./hack/ensure-kubectl.sh
source "${REPO_ROOT}/hack/ensure_kubectl.sh"
# shellcheck source=./hack/e2e/fake-ipa.sh
source "${REPO_ROOT}/hack/e2e/fake-ipa.sh"

"${REPO_ROOT}/hack/ensure_minikube.sh"
"${REPO_ROOT}/hack/ensure_yq.sh"
# Ensure kustomize
make kustomize
sudo install "${REPO_ROOT}/hack/tools/bin/kustomize" /usr/local/bin/

# Extract release version from release-branch name
if [[ "${CAPM3RELEASEBRANCH}" == release-* ]]; then
    CAPM3_RELEASE_PREFIX="${CAPM3RELEASEBRANCH#release-}"
    export CAPM3RELEASE="v${CAPM3_RELEASE_PREFIX}.99"
    export IPAMRELEASE="v${CAPM3_RELEASE_PREFIX}.99"
    export CAPI_RELEASE_PREFIX="v${CAPM3_RELEASE_PREFIX}."
else
    export CAPM3RELEASE="v1.11.99"
    export IPAMRELEASE="v1.11.99"
    export CAPI_RELEASE_PREFIX="v1.11."
    # Hard code CAPI_RELEASE for now, as we are not using the latest CAPI release
    export CAPIRELEASE="v1.11.0-beta.2"
fi

# Default CAPI_CONFIG_FOLDER to $HOME/.config folder if XDG_CONFIG_HOME not set
CONFIG_FOLDER="${XDG_CONFIG_HOME:-$HOME/.config}"
export CAPI_CONFIG_FOLDER="${CONFIG_FOLDER}/cluster-api"

# shellcheck source=./scripts/environment.sh
source "${REPO_ROOT}/scripts/environment.sh"

# Clone dev-env repo
sudo mkdir -p ${WORKING_DIR}
sudo chown "${USER}":"${USER}" ${WORKING_DIR}
M3_DEV_ENV_REPO="https://github.com/metal3-io/metal3-dev-env.git"
M3_DEV_ENV_BRANCH=main
M3_DEV_ENV_PATH="${M3_DEV_ENV_PATH:-${WORKING_DIR}/metal3-dev-env}"
clone_repo "${M3_DEV_ENV_REPO}" "${M3_DEV_ENV_BRANCH}" "${M3_DEV_ENV_PATH}"

# Config devenv
cat <<-EOF >"${M3_DEV_ENV_PATH}/config_${USER}.sh"
export CAPI_VERSION="v1beta2"
export CAPM3_VERSION=${CAPM3_VERSION:-"v1beta1"}
export NUM_NODES=${NUM_NODES:-"4"}
export KUBERNETES_VERSION=${KUBERNETES_VERSION}
export IMAGE_OS=${IMAGE_OS}
export FORCE_REPO_UPDATE="false"
export USE_IRSO="${USE_IRSO:-false}"
EOF

case "${GINKGO_FOCUS:-}" in
  clusterctl-upgrade|k8s-upgrade|basic|integration|remediation|k8s-conformance|capi-md-tests)
    # if running basic, integration, k8s upgrade, clusterctl-upgrade, remediation, k8s conformance or capi-md tests, skip apply bmhs in dev-env
    echo 'export SKIP_APPLY_BMH="true"' >>"${M3_DEV_ENV_PATH}/config_${USER}.sh"
  ;;

  features)
    mkdir -p "${CAPI_CONFIG_FOLDER}"
    echo "ENABLE_BMH_NAME_BASED_PREALLOCATION: true" >"${CAPI_CONFIG_FOLDER}/clusterctl.yaml"
    echo 'export SKIP_APPLY_BMH="true"' >>"${M3_DEV_ENV_PATH}/config_${USER}.sh"
  ;;

  scalability)
    # if running a scalability tests, configure dev-env with fakeIPA
    export NUM_NODES="${NUM_NODES:-100}"
    echo 'export NODES_PLATFORM="fake"' >>"${M3_DEV_ENV_PATH}/config_${USER}.sh"
    echo 'export SKIP_APPLY_BMH="true"' >>"${M3_DEV_ENV_PATH}/config_${USER}.sh"
    sed -i "s/^export NUM_NODES=.*/export NUM_NODES=${NUM_NODES:-100}/" "${M3_DEV_ENV_PATH}/config_${USER}.sh"
    mkdir -p "${CAPI_CONFIG_FOLDER}"
    echo 'CLUSTER_TOPOLOGY: true' >"${CAPI_CONFIG_FOLDER}/clusterctl.yaml"
    echo 'export EPHEMERAL_CLUSTER="minikube"' >>"${M3_DEV_ENV_PATH}/config_${USER}.sh"
  ;;
esac

if [[ ${GINKGO_FOCUS:-} != "scalability" ]]; then
  # Don't run scalability tests if not asked for.
    export GINKGO_SKIP="${GINKGO_SKIP:-} scalability"
fi

# Run make devenv to boot the source cluster
pushd "${M3_DEV_ENV_PATH}" || exit 1
make
popd || exit 1
# Clone BMO repo and install vbmctl
if ! command -v vbmctl >/dev/null 2>&1; then
  # clone_repo "https://github.com/metal3-io/baremetal-operator.git" "main" "${WORKING_DIR}/baremetal-operator"
  # pushd "${WORKING_DIR}/baremetal-operator/test/vbmctl/"
  pushd "${HOME}/baremetal-operator/test/vbmctl"
  go build -tags=e2e,integration -o vbmctl ./main.go
  sudo install vbmctl /usr/local/bin/vbmctl
  popd
fi

# virsh -c qemu:///system net-destroy default || true
# virsh -c qemu:///system net-undefine default || true

DNSMASQ_ENV="${REPO_ROOT}/test/e2e/data/dnsmasq.env"
docker run --name dnsmasq --rm -d --net=host --privileged --user 997:994 \
  --env-file "${DNSMASQ_ENV}" --entrypoint /bin/rundnsmasq \
quay.io/metal3-io/ironic
#
# Set up minikube

VIRSH_NETWORKS=("provisioning")
for network in "${VIRSH_NETWORKS[@]}"; do
  virsh -c qemu:///system net-define "${REPO_ROOT}/hack/e2e/${network}.xml"
  virsh -c qemu:///system net-start "${network}"
  virsh -c qemu:///system net-autostart "${network}"
done

minikube start --driver=kvm2

virsh --connect qemu:///system attach-interface \
  --domain minikube \
  --type network \
  --source provisioning \
  --mac="52:54:00:6c:3c:01" \
  --model virtio \
  --config \
  --persistent

# virsh --connect qemu:///system attach-interface \
#   --domain minikube \
#   --type network \
#   --source provisioning \
#   --mac="52:54:00:6c:3c:02" \
#   --model virtio \
#   --config \
#   --persistent
#
# virsh --connect qemu:///system attach-interface \
#   --type network \
#   --domain minikube \
#   --source external \
#   --model virtio \
#   --config \
#   --persistent
#
# Restart minikube to apply the changes
minikube stop
## Following loop is to avoid minikube restart issue
## https://github.com/kubernetes/minikube/issues/14456
while ! minikube start; do sleep 30; done

sudo ip link set provisioning up

# minikube ssh -- sudo ip route del default via 192.168.111.1 || true


# PROVISIONING_IFACE=$(minikube ssh -- ip -br link | grep "52:54:00:6c:3c:01" | cut -d' ' -f1)
# IRONIC_IFACE=$(minikube ssh -- ip -br link | grep "52:54:00:6c:3c:02" | cut -d' ' -f1)
#
# minikube ssh -- sudo brctl addbr ironicendpoint
# minikube ssh -- sudo ip link set ironicendpoint up
# minikube ssh -- sudo brctl addif ironicendpoint $IRONIC_IFACE
#
# minikube ssh -- sudo ip route del default via 192.168.122.1 || true
# minikube ssh -- sudo ip route del default via 192.168.111.1 dev "${PROVISIONING_IFACE}" || true
# minikube ssh -- sudo ip route del default via 192.168.111.1 dev "${IRONIC_IFACE}" || true
# minikube ssh -- sudo ip route add default via 192.168.122.1 metric 100
# minikube ssh -- sudo ip route add 192.168.111.1 dev "${PROVISIONING_IFACE}" metric 200
# minikube ssh -- sudo ip route add default via 192.168.111.1 dev "${PROVISIONING_IFACE}" metric 300

# minikube ssh -- sudo ip link add link eth2 name eth2.100 type vlan id 100
# minikube ssh -- sudo ip link set eth2.100 up
# minikube ssh -- sudo brctl addbr ironicendpoint
# minikube ssh -- sudo ip link set ironicendpoint up
# minikube ssh -- sudo brctl addif ironicendpoint eth2.100
# minikube ssh -- sudo ip addr add 192.168.111.9/24 dev ironicendpoint

IMAGE_DIR="/tmp/metal3"
mkdir -p "${IMAGE_DIR}/images"

IPA_HEADER_FILE="${IMAGE_DIR}/images/ipa-centos9-master.tar.gz.headers"
if [[ ! -f "${IPA_HEADER_FILE}" ]]; then
  curl -g --dump-header "${IPA_HEADER_FILE}" -O https://tarballs.opendev.org/openstack/ironic-python-agent/dib/ipa-centos9-master.tar.gz
fi

docker run --name image-server-e2e -d \
  -p 8080:8080 \
  -v "${IMAGE_DIR}:/usr/share/nginx/html" nginxinc/nginx-unprivileged

# Attach provisioning interface to minikube with specific mac.
# This will give minikube a known reserved IP address that we can use for Ironic
# virsh -c qemu:///system attach-interface --domain minikube --mac="52:54:00:6c:3c:01" \
# --model virtio --source provisioning --type network --config
#
# virsh -c qemu:///system attach-interface --domain minikube \
# --model virtio --source external --type network --config

# shellcheck disable=SC1091,SC1090
source "${M3_DEV_ENV_PATH}/lib/images.sh"
# shellcheck disable=SC1091,SC1090
source "${M3_DEV_ENV_PATH}/lib/releases.sh"
# shellcheck disable=SC1091,SC1090
source "${M3_DEV_ENV_PATH}/lib/ironic_basic_auth.sh"
# shellcheck disable=SC1091,SC1090
source "${M3_DEV_ENV_PATH}/lib/ironic_tls_setup.sh"
# shellcheck disable=SC1091,SC1090
source "${M3_DEV_ENV_PATH}/lib/common.sh"
# shellcheck disable=SC1091,SC1090
source "${M3_DEV_ENV_PATH}/lib/network.sh"

update_kustomize_image() {
  local image_name="$1"      # e.g., quay.io/metal3-io/ironic
  local env_var_name="$2"    # e.g., IRONIC_IMAGE
  local kustomize_dir="$3"   # e.g., ./overlays/dev

  local full_image="${!env_var_name}"  # Resolve the env var value

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

kustomize_envsubst() {
  local kustomize_dir="$1"
  local file="${kustomize_dir}/kustomization.yaml"

  if [[ ! -f "${file}" ]]; then
    echo "Error: ${file} does not exist."
    return 1
  fi

  local tmp_file
  tmp_file=$(mktemp)

  envsubst < "${file}" > "${tmp_file}" && mv "${tmp_file}" "${file}"
  echo "envsubst applied to ${file}"
}
# E2E_BMCS_CONF_FILE="${REPO_ROOT}/test/e2e/config/bmcs.yaml"
export E2E_BMCS_CONF_FILE="${REPO_ROOT}/test/e2e/config/bmcs-redfish-virtualmedia.yaml"
vbmctl --yaml-source-file "${E2E_BMCS_CONF_FILE}"

# This IP is defined by the network above, and is used consistently in all of
# our e2e overlays
export IRONIC_PROVISIONING_IP="192.168.111.199"

# Start VBMC
docker run --name vbmc --network host -d \
  -v /var/run/libvirt/libvirt-sock:/var/run/libvirt/libvirt-sock \
  -v /var/run/libvirt/libvirt-sock-ro:/var/run/libvirt/libvirt-sock-ro \
  quay.io/metal3-io/vbmc

# Sushy-tools variables
SUSHY_EMULATOR_FILE="${REPO_ROOT}"/test/e2e/data/sushy-tools/sushy-emulator.conf
# Start sushy-tools
docker run --name sushy-tools -d --network host \
  -v "${SUSHY_EMULATOR_FILE}":/etc/sushy/sushy-emulator.conf:Z \
  -v /var/run/libvirt:/var/run/libvirt:Z \
  -e SUSHY_EMULATOR_CONFIG=/etc/sushy/sushy-emulator.conf \
  quay.io/metal3-io/sushy-tools:latest sushy-emulator

# Add ipmi nodes to vbmc
readarray -t BMCS < <(yq e -o=j -I=0 '.[]' "${E2E_BMCS_CONF_FILE}")
for bmc in "${BMCS[@]}"; do
  address=$(echo "${bmc}" | jq -r '.address')
  if [[ "${address}" != ipmi:* ]]; then
    continue
  fi
  hostName=$(echo "${bmc}" | jq -r '.hostName')
  vbmc_port="${address##*:}"
  docker exec vbmc vbmc add "${hostName}" --port "${vbmc_port}" --libvirt-uri "qemu:///system"
  docker exec vbmc vbmc start "${hostName}"
done

# Generate credentials
BMO_OVERLAYS=(
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/release-0.8"
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/release-0.9"
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/release-0.10"
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/pr-test"
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/release-latest"
)
IRONIC_OVERLAYS=(
  "${REPO_ROOT}/test/e2e/data/ironic-deployment/overlays/release-26.0"
  "${REPO_ROOT}/test/e2e/data/ironic-deployment/overlays/release-27.0"
  "${REPO_ROOT}/test/e2e/data/ironic-deployment/overlays/release-29.0"
  "${REPO_ROOT}/test/e2e/data/ironic-deployment/overlays/pr-test"
  "${REPO_ROOT}/test/e2e/data/ironic-deployment/overlays/release-latest"
)

# Update BMO and Ironic images in kustomization.yaml files to use the same image that was used before pivot in the metal3-dev-env
case "${REPO_NAME:-}" in
  baremetal-operator)
    # shellcheck disable=SC2034
    BARE_METAL_OPERATOR_IMAGE="${REGISTRY}/localimages/tested_repo:latest"
    ;;

  ironic-image)
    # shellcheck disable=SC2034
    IRONIC_IMAGE="${REGISTRY}/localimages/tested_repo:latest"
    ;;
esac

update_kustomize_image quay.io/metal3-io/baremetal-operator BARE_METAL_OPERATOR_IMAGE "${REPO_ROOT}"/test/e2e/data/bmo-deployment/overlays/pr-test
update_kustomize_image quay.io/metal3-io/ironic IRONIC_IMAGE "${REPO_ROOT}"/test/e2e/data/ironic-deployment/overlays/pr-test

# Apply envsubst to kustomization.yaml files in BMO and Ironic overlays
kustomize_envsubst "${REPO_ROOT}"/test/e2e/data/bmo-deployment/overlays/pr-test
kustomize_envsubst "${REPO_ROOT}"/test/e2e/data/ironic-deployment/overlays/pr-test

# Create usernames and passwords and other files related to ironi basic auth if they
# are missing
if [[ "${IRONIC_BASIC_AUTH}" == "true" ]]; then
  IRONIC_AUTH_DIR="${IRONIC_AUTH_DIR:-${IRONIC_DATA_DIR}/auth}"
  mkdir -p "${IRONIC_AUTH_DIR}"

  # If usernames and passwords are unset, read them from file or generate them
  if [[ -z "${IRONIC_USERNAME:-}" ]]; then
    if [[ ! -f "${IRONIC_AUTH_DIR}/ironic-username" ]]; then
      IRONIC_USERNAME="$(uuidgen)"
      echo "${IRONIC_USERNAME}" > "${IRONIC_AUTH_DIR}/ironic-username"
    else
      IRONIC_USERNAME="$(cat "${IRONIC_AUTH_DIR}/ironic-username")"
    fi
  fi
  if [[ -z "${IRONIC_PASSWORD:-}" ]]; then
    if [ ! -f "${IRONIC_AUTH_DIR}/ironic-password" ]; then
      IRONIC_PASSWORD="$(uuidgen)"
      echo "${IRONIC_PASSWORD}" > "${IRONIC_AUTH_DIR}/ironic-password"
    else
      IRONIC_PASSWORD="$(cat "${IRONIC_AUTH_DIR}/ironic-password")"
    fi
  fi

  export IRONIC_USERNAME
  export IRONIC_PASSWORD
fi

for overlay in "${BMO_OVERLAYS[@]}"; do
  echo "${IRONIC_USERNAME}" > "${overlay}/ironic-username"
  echo "${IRONIC_PASSWORD}" > "${overlay}/ironic-password"
done

for overlay in "${IRONIC_OVERLAYS[@]}"; do
  echo "IRONIC_HTPASSWD=$(htpasswd -n -b -B "${IRONIC_USERNAME}" "${IRONIC_PASSWORD}")" > \
    "${overlay}/ironic-htpasswd"
  envsubst < "${REPO_ROOT}/test/e2e/data/ironic-deployment/components/basic-auth/ironic-auth-config-tpl" > \
  "${overlay}/ironic-auth-config"
done

# run e2e tests
if [[ -n "${CLUSTER_TOPOLOGY:-}" ]]; then
  export CLUSTER_TOPOLOGY=true
  make e2e-clusterclass-tests
else
  make e2e-tests
fi
