eval "$(go env)"
export GOPATH
export IMAGE_OS="Ubuntu"
export CONTAINER_RUNTIME="docker"
export EPHEMERAL_CLUSTER="kind"
export CAPI_VERSION="v1alpha3"
export CAPM3_VERSION="v1alpha4"
export NUM_OF_MASTER_REPLICAS=3
export NUM_OF_WORKER_REPLICAS=1
export NUM_NODES=4
export M3PATH="${GOPATH}/src/github.com/metal3-io"
export BMOPATH="${M3PATH}/baremetal-operator"
export NAMESPACE="metal3"
export IRONIC_DATA_DIR="/opt/metal3-dev-env/ironic"
export IRONIC_TLS_SETUP="true"
export IRONIC_BASIC_AUTH="true"
