ARG USER=meridio
ARG UID=10001
ARG HOME=/app

# BIRD version. Built from source because no Alpine release packages this version
# yet (verified against alpine 3.21-3.24 and edge). Building on an Alpine builder
# (same musl libc as the runtime stage) so the resulting binaries run on the
# Alpine runtime below. Minimum 3.3.1 is required for the TCP-AO MAC algorithms
# declared in api/v1alpha1/gatewayrouter_types.go.
ARG BIRD_VERSION=3.3.2

# BIRD builder stage: compile bird/birdc from source against Alpine/musl.
FROM alpine AS bird-builder
ARG BIRD_VERSION
ARG TARGETARCH
RUN apk add --no-cache \
    build-base \
    flex \
    bison \
    ncurses-dev \
    readline-dev \
    autoconf \
    automake \
    libtool \
    m4 \
    linux-headers \
    wget \
    ca-certificates
WORKDIR /tmp
# Build portably: BIRD's configure defaults to host-CPU tuning (-march=native),
# which produces instructions that SIGILL ("illegal instruction") on nodes with a
# different/older CPU than the build host. Pin a portable baseline arch (per target
# architecture) and strip the result so it runs on any node of that arch.
RUN case "${TARGETARCH}" in \
        amd64) ARCH_CFLAGS="-march=x86-64 -mtune=generic" ;; \
        arm64) ARCH_CFLAGS="-march=armv8-a -mtune=generic" ;; \
        *)     ARCH_CFLAGS="" ;; \
    esac \
    && wget "https://github.com/CZ-NIC/bird/archive/refs/tags/v${BIRD_VERSION}.tar.gz" \
    && tar -xzf "v${BIRD_VERSION}.tar.gz" \
    && cd "bird-${BIRD_VERSION}" \
    && autoreconf -i \
    && ./configure CFLAGS="-O2 -g0 ${ARCH_CFLAGS}" \
    && make -j"$(nproc)" \
    && make install \
    && strip /usr/local/sbin/bird /usr/local/sbin/birdc

FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

COPY go.mod go.mod
COPY go.sum go.sum

RUN go mod download

COPY . .

# Build
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -ldflags="-extldflags -static \
              -X github.com/nordix/meridio-2/internal/common/version.Version=${VERSION} \
              -X github.com/nordix/meridio-2/internal/common/version.GitCommit=${GIT_COMMIT} \
              -X github.com/nordix/meridio-2/internal/common/version.BuildDate=${BUILD_DATE}" \
    -a -o router ./cmd/router

# Runtime stage
FROM alpine
ARG USER
ARG UID
ARG HOME
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

RUN apk update && apk add iproute2 tcpdump nftables iputils net-tools libcap readline \
  && setcap 'cap_sys_ptrace,cap_dac_override+ep' /sbin/ss \
  && setcap 'cap_sys_ptrace,cap_dac_override+ep' /bin/netstat \
  && setcap 'cap_net_raw+ep' /bin/ping \
  && setcap 'cap_net_raw+ep' /usr/bin/tcpdump

# BIRD built from source in the bird-builder stage (see top of file). Installed to
# /usr/local/sbin by "make install"; birdc needs readline at runtime (added above).
COPY --from=bird-builder /usr/local/sbin/bird /usr/local/sbin/bird
COPY --from=bird-builder /usr/local/sbin/birdc /usr/local/sbin/birdc

RUN mkdir -p /run/bird && mkdir -p /etc/bird
RUN addgroup --gid $UID $USER \
  && adduser $USER --home $HOME --uid $UID -G $USER --disabled-password \
  && chown -R :root "${HOME}" && chmod -R g+s=u "${HOME}"
WORKDIR $HOME

COPY --from=builder /workspace/router .

RUN setcap 'cap_net_admin+ep' ./router \
  && setcap 'cap_net_admin,cap_net_bind_service,cap_net_raw+ep' /usr/local/sbin/bird
USER ${UID}:${UID}

LABEL org.opencontainers.image.revision="${GIT_COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

ENTRYPOINT ["./router", "run"]
