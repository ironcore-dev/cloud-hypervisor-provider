# Build the manager binary
FROM --platform=$BUILDPLATFORM golang:1.23 AS builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/ cmd/
COPY internal/ internal/

ARG TARGETOS
ARG TARGETARCH

RUN mkdir bin


FROM builder AS cloud-hypervisor-provider-builder

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    GOOS=$TARGETOS GOARCH=$TARGETARCH GO111MODULE=on go build -ldflags="-s -w" -a -o bin/cloud-hypervisor-provider ./cmd/cloud-hypervisor-provider/main.go

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    GOOS=$TARGETOS GOARCH=$TARGETARCH GO111MODULE=on go install github.com/ironcore-dev/ironcore/irictl-machine/cmd/irictl-machine@main


# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details

FROM debian:bullseye-slim AS cloud-hypervisor-provider
WORKDIR /
COPY --from=cloud-hypervisor-provider-builder /workspace/bin/cloud-hypervisor-provider .
COPY --from=cloud-hypervisor-provider-builder /go/bin/irictl-machine .
USER 65532:65532

ENTRYPOINT ["/cloud-hypervisor-provider"]


