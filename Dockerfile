# Build the manager binary
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG BUILDPLATFORM
ENV BUILDARCH=${BUILDPLATFORM##*/}

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# Cache deps before building and copying source
RUN --mount=type=cache,target=/go/pkg \
    go mod download

# Copy the go source
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
COPY cloud-hypervisor/ cloud-hypervisor/

RUN mkdir bin

# Build the cloud-hypervisor-provider
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH  \
    go build -ldflags="-s -w" -a -o bin/cloud-hypervisor-provider ./cmd/cloud-hypervisor-provider/main.go

# Install irictl-machine
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go install github.com/ironcore-dev/ironcore/irictl-machine/cmd/irictl-machine@main

# Ensure the binary is in a common location
RUN if [ "$TARGETARCH" = "$BUILDARCH" ]; then \
        mv /go/bin/irictl-machine /workspace/bin/irictl-machine; \
    else \
        mv /go/bin/linux_$TARGETARCH/irictl-machine /workspace/bin/irictl-machine; \
    fi


# Use distroless as minimal base image to package the manager binary
FROM debian:bullseye-slim AS cloud-hypervisor-provider
WORKDIR /

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

# Copy the binaries from the builder
COPY --from=builder /workspace/bin/cloud-hypervisor-provider .
COPY --from=builder /workspace/bin/irictl-machine .

ENTRYPOINT ["/cloud-hypervisor-provider"]



FROM builder AS prepare-host-builder

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -a -o bin/prepare-host \
    ./cmd/prepare-host


FROM alpine:3.23 AS prepare-host
RUN apk add --no-cache ca-certificates

WORKDIR /
COPY --from=prepare-host-builder /workspace/bin/prepare-host .
USER 65532:65532

ENTRYPOINT ["/prepare-host"]