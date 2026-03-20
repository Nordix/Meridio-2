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
    -a -o network-sidecar cmd/network-sidecar/main.go

# Runtime stage
FROM alpine:3.22
ARG USER
ARG UID
ARG HOME=/home/${USER}

RUN apk add --no-cache libcap \
  && addgroup -g ${UID} ${USER} \
  && adduser -D -h ${HOME} -u ${UID} -G ${USER} ${USER} \
  && chown -R :root "${HOME}" && chmod -R g+s=u "${HOME}"

WORKDIR ${HOME}
COPY --from=builder /workspace/network-sidecar .
RUN setcap 'cap_net_admin+ep' ./network-sidecar

USER ${UID}:${UID}

ENTRYPOINT ["./network-sidecar", "run"]
