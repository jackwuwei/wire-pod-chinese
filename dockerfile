# syntax=docker/dockerfile:1.6

FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG SHERPA_ONNX_VERSION=1.12.40
ARG COMMIT_SHA=unknown

ENV DEBIAN_FRONTEND=noninteractive \
    CGO_ENABLED=1 \
    GOPROXY=https://goproxy.cn,direct \
    GOMODCACHE=/go/pkg/mod

RUN set -eux; \
    if [ -f /etc/apt/sources.list.d/debian.sources ]; then \
        sed -i 's|deb.debian.org|mirrors.tuna.tsinghua.edu.cn|g; s|security.debian.org/debian-security|mirrors.tuna.tsinghua.edu.cn/debian-security|g' /etc/apt/sources.list.d/debian.sources; \
    fi; \
    if [ -f /etc/apt/sources.list ]; then \
        sed -i 's|deb.debian.org|mirrors.tuna.tsinghua.edu.cn|g; s|security.debian.org/debian-security|mirrors.tuna.tsinghua.edu.cn/debian-security|g' /etc/apt/sources.list; \
    fi

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        build-essential \
        bzip2 \
        ca-certificates \
        curl \
        git \
        libasound2-dev \
        libopus-dev \
        libopusfile-dev \
        libsox-dev \
        libsodium-dev \
        pkg-config \
        unzip \
        wget \
        gcc-aarch64-linux-gnu \
        g++-aarch64-linux-gnu \
        gcc-arm-linux-gnueabihf \
        g++-arm-linux-gnueabihf; \
    if [ "${TARGETARCH}" = "arm64" ]; then \
        dpkg --add-architecture arm64; \
        apt-get update; \
        apt-get install -y --no-install-recommends \
            libasound2-dev:arm64 \
            libopus-dev:arm64 \
            libopusfile-dev:arm64 \
            libsox-dev:arm64 \
            libsodium-dev:arm64; \
    elif [ "${TARGETARCH}" = "arm" ]; then \
        dpkg --add-architecture armhf; \
        apt-get update; \
        apt-get install -y --no-install-recommends \
            libasound2-dev:armhf \
            libopus-dev:armhf \
            libopusfile-dev:armhf \
            libsox-dev:armhf \
            libsodium-dev:armhf; \
    fi; \
    rm -rf /var/lib/apt/lists/*

WORKDIR /src

COPY chipper/go.mod chipper/go.sum ./chipper/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd chipper && go mod download

COPY . .

RUN find . -type f -name '*.sh' -exec sed -i 's/\r$//' {} +

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    BUILD_COMMIT="${COMMIT_SHA}"; \
    if [ "${BUILD_COMMIT}" = "unknown" ] || [ -z "${BUILD_COMMIT}" ]; then \
        BUILD_COMMIT=$(git rev-parse --short HEAD || echo "dev"); \
    fi; \
    GOOS_VALUE="${TARGETOS}"; \
    GOARCH_VALUE="${TARGETARCH}"; \
    if [ -z "${GOOS_VALUE}" ]; then GOOS_VALUE=$(go env GOOS); fi; \
    if [ -z "${GOARCH_VALUE}" ]; then GOARCH_VALUE=$(go env GOARCH); fi; \
    if [ "${TARGETARCH}" = "arm" ]; then \
        GOARM_VALUE="${TARGETVARIANT#v}"; \
        if [ -z "${GOARM_VALUE}" ]; then GOARM_VALUE=7; fi; \
        export GOARM=${GOARM_VALUE}; \
    fi; \
    mkdir -p /build; \
    cd /src/chipper; \
    if [ "${GOARCH_VALUE}" = "arm64" ]; then \
        export CC=aarch64-linux-gnu-gcc CXX=aarch64-linux-gnu-g++; \
        SHERPA_LIB_TRIPLET=aarch64-unknown-linux-gnu; \
    elif [ "${GOARCH_VALUE}" = "arm" ]; then \
        export CC=arm-linux-gnueabihf-gcc CXX=arm-linux-gnueabihf-g++; \
        SHERPA_LIB_TRIPLET=arm-unknown-linux-gnueabihf; \
    else \
        SHERPA_LIB_TRIPLET=x86_64-unknown-linux-gnu; \
    fi; \
    LIB_DIR="/usr/lib/x86_64-linux-gnu"; \
    if [ "${GOARCH_VALUE}" = "arm64" ]; then LIB_DIR="/usr/lib/aarch64-linux-gnu"; fi; \
    if [ "${GOARCH_VALUE}" = "arm" ]; then LIB_DIR="/usr/lib/arm-linux-gnueabihf"; fi; \
    export PKG_CONFIG_PATH="${LIB_DIR}/pkgconfig"; \
    export PKG_CONFIG_LIBDIR="${PKG_CONFIG_PATH}"; \
    GOOS=${GOOS_VALUE} GOARCH=${GOARCH_VALUE} \
    go build -tags "nolibopusfile" -ldflags "-s -w -X github.com/kercre123/wire-pod/chipper/pkg/vars.CommitSHA=${BUILD_COMMIT}" \
        -o /build/chipper ./cmd/sherpa-onnx; \
    echo "${BUILD_COMMIT}" >/build/.wirepod-version; \
    mkdir -p /build/sherpa-onnx-libs; \
    cp -a /go/pkg/mod/github.com/k2-fsa/sherpa-onnx-go-linux@v${SHERPA_ONNX_VERSION}/lib/${SHERPA_LIB_TRIPLET} /build/sherpa-onnx-libs/${SHERPA_LIB_TRIPLET}


FROM ubuntu:22.04 AS runtime

ARG SHERPA_ONNX_VERSION=1.12.40
ARG COMMIT_SHA=unknown

ENV DEBIAN_FRONTEND=noninteractive \
    WIREPOD_DATA_DIR=/data

RUN sed -i 's|archive.ubuntu.com/ubuntu|mirrors.tuna.tsinghua.edu.cn/ubuntu|g; s|security.ubuntu.com/ubuntu|mirrors.tuna.tsinghua.edu.cn/ubuntu|g; s|ports.ubuntu.com/ubuntu-ports|mirrors.tuna.tsinghua.edu.cn/ubuntu-ports|g' /etc/apt/sources.list

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        avahi-daemon \
        avahi-utils \
        bash \
        bzip2 \
        ca-certificates \
        curl \
        git \
        iproute2 \
        libasound2 \
        libatomic1 \
        libopus0 \
        libopusfile0 \
        libsodium23 \
        libsox3 \
        libstdc++6 \
        python3 \
        python3-venv \
        tzdata \
        unzip \
        wget \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/wire-pod

COPY --from=builder /src /opt/wire-pod
COPY --from=builder /build/chipper /opt/wire-pod/chipper/chipper
COPY --from=builder /build/.wirepod-version /opt/wire-pod/.wirepod-version

# The sherpa-onnx binary embeds an absolute rpath pointing into the builder's GOMODCACHE.
# Re-create that path in the runtime image so libsherpa-onnx-c-api.so / libonnxruntime.so resolve.
COPY --from=builder /build/sherpa-onnx-libs/ /go/pkg/mod/github.com/k2-fsa/sherpa-onnx-go-linux@v${SHERPA_ONNX_VERSION}/lib/

RUN python3 -m venv /opt/wire-pod/chipper/.venv \
    && /opt/wire-pod/chipper/.venv/bin/pip install --no-cache-dir -i https://pypi.tuna.tsinghua.edu.cn/simple/ --upgrade pip \
    && /opt/wire-pod/chipper/.venv/bin/pip install --no-cache-dir -i https://pypi.tuna.tsinghua.edu.cn/simple/ edge-tts

RUN chmod +x \
        /opt/wire-pod/setup.sh \
        /opt/wire-pod/update.sh \
        /opt/wire-pod/chipper/start.sh \
        /opt/wire-pod/docker/entrypoint.sh

VOLUME ["/data"]

EXPOSE 80 443 8080 8084

LABEL org.opencontainers.image.revision="${COMMIT_SHA}"

ENTRYPOINT ["/opt/wire-pod/docker/entrypoint.sh"]
CMD ["/opt/wire-pod/chipper/start.sh"]
