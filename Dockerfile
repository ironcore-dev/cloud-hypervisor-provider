# Build the manager binary
FROM --platform=$BUILDPLATFORM golang:1.23 AS builder
ARG TARGETOS
ARG TARGETARCH

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

RUN mkdir bin

FROM builder AS cloud-hypervisor-provider-builder

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GO111MODULE=on  \
    go build -ldflags="-s -w" -a -o bin/cloud-hypervisor-provider ./cmd/cloud-hypervisor-provider/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM debian:bullseye-slim AS cloud-hypervisor-provider
WORKDIR /
COPY --from=cloud-hypervisor-provider-builder /workspace/bin/cloud-hypervisor-provider .
USER 65532:65532

ENTRYPOINT ["/cloud-hypervisor-provider"]


