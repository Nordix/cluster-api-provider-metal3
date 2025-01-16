#!/bin/bash

set -euxo pipefail

REPO_ROOT=$(realpath "$(dirname "$(realpath "${BASH_SOURCE[0]}")")"/..)
cd "${REPO_ROOT}" || exit 1
export CAPM3PATH="${REPO_ROOT}"
export WORKING_DIR=/tmp/metal3-dev-env
FORCE_REPO_UPDATE="${FORCE_REPO_UPDATE:-false}"

export CAPM3RELEASEBRANCH="${CAPM3RELEASEBRANCH:-main}"

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

# Extract release version from release-branch name
export CAPM3RELEASE="v1.10.99"
export CAPI_RELEASE_PREFIX="v1.9."
if [[ "${CAPM3RELEASEBRANCH}" == release-* ]]; then
    CAPM3_RELEASE_PREFIX="${CAPM3RELEASEBRANCH#release-}"
    export CAPM3RELEASE="v${CAPM3_RELEASE_PREFIX}.99"
    export CAPI_RELEASE_PREFIX="v${CAPM3_RELEASE_PREFIX}."
fi

# Default CAPI_CONFIG_FOLDER to $HOME/.config folder if XDG_CONFIG_HOME not set
CONFIG_FOLDER="${XDG_CONFIG_HOME:-$HOME/.config}"
export CAPI_CONFIG_FOLDER="${CONFIG_FOLDER}/cluster-api"

# shellcheck source=./scripts/environment.sh
source "${REPO_ROOT}/scripts/environment.sh"

# Clone BMO repo and install vbmctl
if ! command -v vbmctl >/dev/null 2>&1; then
  clone_repo "https://github.com/metal3-io/baremetal-operator.git" "main" "${WORKING_DIR}/baremetal-operator"
  pushd "${WORKING_DIR}/baremetal-operator/test/vbmctl/"
  go build -tags=e2e,integration -o vbmctl ./main.go
  sudo install vbmctl /usr/local/bin/vbmctl
  popd
fi

# Set up minikube
minikube start --driver=kvm2

virsh -c qemu:///system net-define "${REPO_ROOT}/hack/e2e/net.xml"
virsh -c qemu:///system net-start baremetal-e2e
# Attach baremetal-e2e interface to minikube with specific mac.
# This will give minikube a known reserved IP address that we can use for Ironic
virsh -c qemu:///system attach-interface --domain minikube --mac="52:54:00:6c:3c:01" \
  --model virtio --source baremetal-e2e --type network --config

# Restart minikube to apply the changes
minikube stop
## Following loop is to avoid minikube restart issue
## https://github.com/kubernetes/minikube/issues/14456
while ! minikube start; do sleep 30; done

E2E_BMCS_CONF_FILE="${REPO_ROOT}/test/e2e/config/bmcs.yaml"
vbmctl --yaml-source-file "${E2E_BMCS_CONF_FILE}"

# This IP is defined by the network above, and is used consistently in all of
# our e2e overlays
export IRONIC_PROVISIONING_IP="192.168.222.199"

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
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/release-0.6"
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/release-0.8"
  "${REPO_ROOT}/test/e2e/data/bmo-deployment/overlays/release-latest"
)
IRONIC_OVERLAYS=(
  "${REPO_ROOT}/test/e2e/data/ironic-deployment/overlays/release-25.0"
  "${REPO_ROOT}/test/e2e/data/ironic-deployment/overlays/release-26.0"
  "${REPO_ROOT}/test/e2e/data/ironic-deployment/overlays/release-latest"
)

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
  IRONIC_INSPECTOR_USERNAME="${IRONIC_INSPECTOR_USERNAME:-${IRONIC_USERNAME}}"
  IRONIC_INSPECTOR_PASSWORD="${IRONIC_INSPECTOR_PASSWORD:-${IRONIC_PASSWORD}}"

  export IRONIC_USERNAME
  export IRONIC_PASSWORD
  export IRONIC_INSPECTOR_USERNAME
  export IRONIC_INSPECTOR_PASSWORD
fi

for overlay in "${BMO_OVERLAYS[@]}"; do
  echo "${IRONIC_USERNAME}" > "${overlay}/ironic-username"
  echo "${IRONIC_PASSWORD}" > "${overlay}/ironic-password"
  if [[ "${overlay}" =~ release-0\.[1-5]$ ]]; then
    echo "${IRONIC_INSPECTOR_USERNAME}" > "${overlay}/ironic-inspector-username"
    echo "${IRONIC_INSPECTOR_PASSWORD}" > "${overlay}/ironic-inspector-password"
  fi
done

for overlay in "${IRONIC_OVERLAYS[@]}"; do
  echo "IRONIC_HTPASSWD=$(htpasswd -n -b -B "${IRONIC_USERNAME}" "${IRONIC_PASSWORD}")" > \
    "${overlay}/ironic-htpasswd"
  envsubst < "${REPO_ROOT}/test/e2e/data/ironic-deployment/components/basic-auth/ironic-auth-config-tpl" > \
  "${overlay}/ironic-auth-config"
  IRONIC_INSPECTOR_AUTH_CONFIG_TPL="/tmp/ironic-inspector-auth-config-tpl"
  curl -o "${IRONIC_INSPECTOR_AUTH_CONFIG_TPL}" https://raw.githubusercontent.com/metal3-io/baremetal-operator/release-0.5/ironic-deployment/components/basic-auth/ironic-inspector-auth-config-tpl
  envsubst < "${IRONIC_INSPECTOR_AUTH_CONFIG_TPL}" > \
    "${overlay}/ironic-inspector-auth-config"
  echo "INSPECTOR_HTPASSWD=$(htpasswd -n -b -B "${IRONIC_INSPECTOR_USERNAME}" \
    "${IRONIC_INSPECTOR_PASSWORD}")" > "${overlay}/ironic-inspector-htpasswd"
done

# run e2e tests
if [[ -n "${CLUSTER_TOPOLOGY:-}" ]]; then
  export CLUSTER_TOPOLOGY=true
  make e2e-clusterclass-tests
else
  make e2e-tests
fi
