#!/usr/bin/env bash

# Copyright 2021 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

GOPATH_BIN="$(go env GOPATH)/bin/"
MINIMUM_KUBECTL_VERSION=${KUBERNETES_VERSION:-"v1.36.0"}

# Ensure the kubectl tool exists and is a viable version, or installs it
verify_kubectl_version()
{
    # Remove any broken kubectl binary in GOPATH/bin
    if [[ -f "${GOPATH_BIN}/kubectl" ]] && ! "${GOPATH_BIN}/kubectl" version --client &>/dev/null; then
        echo "Removing broken kubectl binary at ${GOPATH_BIN}/kubectl"
        rm -f "${GOPATH_BIN}/kubectl"
    fi

    # If kubectl is not available on the path, or not a working binary, get it
    if ! kubectl version --client &>/dev/null; then
        if [[ "${OSTYPE}" == "linux-gnu" ]]; then
            if ! [ -d "${GOPATH_BIN}" ]; then
                mkdir -p "${GOPATH_BIN}"
            fi
            echo 'kubectl not found or not working, installing'
            curl -sLo "${GOPATH_BIN}/kubectl" "https://dl.k8s.io/release/${MINIMUM_KUBECTL_VERSION}/bin/linux/amd64/kubectl"
            chmod +x "${GOPATH_BIN}/kubectl"
        else
            echo "Missing required binary in path: kubectl"
            return 2
        fi
    fi

    local kubectl_version
    kubectl_version="$(kubectl version --client -o yaml 2>/dev/null | grep -F gitVersion | awk '{print $2}')" || true
    if [[ -z "${kubectl_version}" ]]; then
        # Fallback: try parsing non-yaml output (e.g. "Client Version: v1.36.0")
        kubectl_version="$(kubectl version --client 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+')" || true
    fi
    if [[ -z "${kubectl_version}" ]]; then
        echo "WARNING: Unable to determine kubectl version, skipping version check"
        return 0
    fi
    if [[ "${MINIMUM_KUBECTL_VERSION}" != $(echo -e "${MINIMUM_KUBECTL_VERSION}\n${kubectl_version}" | sort -s -t. -k 1,1 -k 2,2n -k 3,3n | head -n1) ]]; then
        cat << EOF
Detected kubectl version: ${kubectl_version}.
Requires ${MINIMUM_KUBECTL_VERSION} or greater.
Please install ${MINIMUM_KUBECTL_VERSION} or later.
EOF
        return 2
    fi
}

verify_kubectl_version
