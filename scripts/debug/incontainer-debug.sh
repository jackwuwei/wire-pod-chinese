#!/usr/bin/env bash
# Run INSIDE the wire-pod container after compose.debug.yaml is active.
# - Installs Go + dlv into the persistent /opt/dev-tools volume on first run.
# - Installs C dev headers needed for cgo (libopus, sherpa-onnx already in /go/pkg/mod).
# - Rebuilds chipper with debug flags from the bind-mounted source at /opt/wire-pod-debug.
# - Stops any prior dlv, then launches dlv headless on 0.0.0.0:2345.
set -euo pipefail

GO_VERSION="${GO_VERSION:-1.22.10}"
GO_TARBALL="go${GO_VERSION}.linux-amd64.tar.gz"
TOOLS_DIR="/opt/dev-tools"
GO_ROOT="${TOOLS_DIR}/go"
GOPATH_DIR="${TOOLS_DIR}/gopath"
DLV_BIN="${GOPATH_DIR}/bin/dlv"
SRC_DIR="/opt/wire-pod-debug"
RUN_DIR="/opt/wire-pod/chipper"
BIN_PATH="${SRC_DIR}/chipper/chipper.debug"
DLV_LISTEN="${DLV_LISTEN:-0.0.0.0:2345}"
STT_CMD_PKG="${STT_CMD_PKG:-./cmd/sherpa-onnx}"

export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export GOFLAGS="${GOFLAGS:--mod=mod}"
export GOPATH="${GOPATH_DIR}"
export PATH="${GO_ROOT}/bin:${GOPATH_DIR}/bin:${PATH}"
export DEBIAN_FRONTEND=noninteractive

log() { printf '[debug-setup] %s\n' "$*"; }

if [ ! -d "${SRC_DIR}/chipper" ]; then
    echo "ERROR: ${SRC_DIR} is empty. Did the bind mount work? Re-run compose with compose.debug.yaml." >&2
    exit 1
fi

mkdir -p "${TOOLS_DIR}" "${GOPATH_DIR}/bin"

if [ ! -x "${GO_ROOT}/bin/go" ]; then
    log "Installing Go ${GO_VERSION} to ${GO_ROOT}"
    apt-get update -qq
    apt-get install -y --no-install-recommends curl ca-certificates >/dev/null
    tmp_tar="$(mktemp -d)/${GO_TARBALL}"
    for url in \
        "https://golang.google.cn/dl/${GO_TARBALL}" \
        "https://go.dev/dl/${GO_TARBALL}"; do
        log "  fetching $url"
        if curl -fsSL --connect-timeout 30 -o "$tmp_tar" "$url"; then
            break
        fi
        rm -f "$tmp_tar"
    done
    test -s "$tmp_tar" || { echo "Go download failed" >&2; exit 1; }
    rm -rf "${GO_ROOT}"
    mkdir -p "${TOOLS_DIR}"
    tar -C "${TOOLS_DIR}" -xzf "$tmp_tar"
    rm -f "$tmp_tar"
fi

if [ ! -x "${DLV_BIN}" ]; then
    log "Installing dlv to ${DLV_BIN}"
    "${GO_ROOT}/bin/go" install github.com/go-delve/delve/cmd/dlv@latest
fi

# C headers — sherpa-onnx libs/headers come from /go/pkg/mod (already in image),
# but cgo for libopus/libopusfile needs dev headers + pkg-config.
need_pkgs=()
for p in build-essential pkg-config libopus-dev libopusfile-dev libsodium-dev libsox-dev libasound2-dev; do
    dpkg -s "$p" >/dev/null 2>&1 || need_pkgs+=("$p")
done
if [ "${#need_pkgs[@]}" -gt 0 ]; then
    log "Installing apt deps: ${need_pkgs[*]}"
    apt-get update -qq
    apt-get install -y --no-install-recommends "${need_pkgs[@]}" >/dev/null
fi

log "Building chipper.debug from ${SRC_DIR}/chipper"
cd "${SRC_DIR}/chipper"
COMMIT="$(cat /opt/wire-pod/.wirepod-version 2>/dev/null || echo debug)"
go build \
    -tags nolibopusfile \
    -gcflags="all=-N -l" \
    -ldflags "-X github.com/kercre123/wire-pod/chipper/pkg/vars.CommitSHA=${COMMIT}" \
    -o "${BIN_PATH}" \
    "${STT_CMD_PKG}"

log "Stopping any prior dlv / chipper.debug"
pkill -f "dlv .* ${BIN_PATH}" 2>/dev/null || true
pkill -f "${BIN_PATH}" 2>/dev/null || true
sleep 0.5

log "Loading runtime env from ${RUN_DIR}/source.sh"
# shellcheck disable=SC1091
source "${RUN_DIR}/source.sh"

cd "${RUN_DIR}"
log "Launching dlv on ${DLV_LISTEN} (cwd=${RUN_DIR}, exec=${BIN_PATH})"
exec "${DLV_BIN}" exec "${BIN_PATH}" \
    --listen="${DLV_LISTEN}" \
    --headless \
    --api-version=2 \
    --accept-multiclient \
    --check-go-version=false \
    --continue
