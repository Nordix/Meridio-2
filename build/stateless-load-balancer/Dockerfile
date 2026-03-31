# Global build args
ARG USER=meridio
ARG UID=10005

# Build stage
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/
COPY hack/ hack/

# Build
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -ldflags="-extldflags -static \
              -X github.com/nordix/meridio-2/internal/common/version.Version=${VERSION} \
              -X github.com/nordix/meridio-2/internal/common/version.GitCommit=${GIT_COMMIT} \
              -X github.com/nordix/meridio-2/internal/common/version.BuildDate=${BUILD_DATE}" \
    -a -o stateless-load-balancer cmd/stateless-load-balancer/main.go

# NFQLB build stage
FROM ubuntu:22.04 AS nfqlb-build

RUN apt-get update && apt install -y git build-essential libmnl-dev libnetfilter-queue-dev libpcap-dev curl \
    && git clone --depth 1 --branch 1.2.0 https://github.com/Nordix/nfqueue-loadbalancer.git

WORKDIR /nfqueue-loadbalancer

ENV ARCHIVE=/tmp

RUN curl -L https://netfilter.org/projects/libmnl/files/libmnl-1.0.4.tar.bz2 > /tmp/libmnl-1.0.4.tar.bz2 \
    && chmod 777 /tmp/* \
    && /nfqueue-loadbalancer/nfqlb.sh libmnl_unpack \
    && /nfqueue-loadbalancer/nfqlb.sh libmnl_build \
    && make -C src -j8 X=/bin/nfqlb static

# Runtime stage
FROM alpine:3.22
ARG USER
ARG UID
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

RUN apk update && apk add iproute2 tcpdump nftables libcap

RUN addgroup -g ${UID} ${USER} \
  && adduser -D -u ${UID} -G ${USER} ${USER}

WORKDIR /app
COPY --from=builder /workspace/stateless-load-balancer .
COPY --from=nfqlb-build /bin/nfqlb /bin/nfqlb

# Set capabilities for network operations
RUN setcap 'cap_net_admin+ep' /app/stateless-load-balancer \
  && chown root:root /bin/nfqlb && setcap 'cap_net_admin,cap_ipc_lock,cap_ipc_owner+ep' /bin/nfqlb \
  && setcap 'cap_net_admin+ep' /usr/sbin/nft

USER ${UID}:${UID}

LABEL org.opencontainers.image.revision="${GIT_COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

ENTRYPOINT ["/app/stateless-load-balancer", "run"]
