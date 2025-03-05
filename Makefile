# Image URL to use all building/pushing image targets
CLOUD_HYPERVISOR_PROVIDER_IMG ?= cloud-hypervisor-provider:latest


# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.31

# Docker image name for the mkdocs based local development setup
IMAGE=ironcore/documentation

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
FILE="config/machinepoollet-broker/broker-rbac/role.yaml"
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role paths="./..." output:rbac:artifacts:config=config/rbac


.PHONY: generate
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: goimports ## Run goimports against code.
	$(GOIMPORTS) -w .

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: lint
lint: golangci-lint ## Run golangci-lint on the code.
	$(GOLANGCI_LINT) run ./...

.PHONY: clean
clean: ## Clean any artifacts that can be regenerated.
	rm -rf client-go/applyconfigurations
	rm -rf client-go/informers
	rm -rf client-go/listers
	rm -rf client-go/ironcore
	rm -rf client-go/openapi

.PHONY: add-license
add-license: addlicense ## Add license headers to all go files.
	find . -name '*.go' -exec $(ADDLICENSE) -f hack/license-header.txt {} +

.PHONY: check-license
check-license: addlicense ## Check that every file has a license header present.
	find . -name '*.go' -exec $(ADDLICENSE) -check -c 'IronCore authors' {} +

.PHONY: check
check: generate manifests add-license fmt lint test # Generate manifests, code, lint, add licenses, test


.PHONY: start-docs
start-docs: ## Start the local mkdocs based development environment.
	docker build -t $(IMAGE) -f docs/Dockerfile . --load
	docker run -p 8000:8000 -v `pwd`/:/docs $(IMAGE)

.PHONY: clean-docs
clean-docs: ## Remove all local mkdocs Docker images (cleanup).
	docker container prune --force --filter "label=project=ironcore_documentation"

.PHONY: test
test: generate manifests fmt vet test-only ## Run tests.

.PHONY: test-only
test-only: envtest ## Run *only* the tests - no generation, linting etc.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test ./... -coverprofile cover.out


##@ Build

.PHONY: build
build: generate fmt vet ## Build manager binary.
	go build -o bin/cloud-hypervisor-provider ./cmd/cloud-hypervisor-provider

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/cloud-hypervisor-provider

.PHONY: docker-build
docker-build: \
	docker-build-cloud-hypervisor-provider


.PHONY: docker-build-cloud-hypervisor-provider
docker-build-cloud-hypervisor-provider: ## Build cloud-hypervisor-provider image.
	docker build --target cloud-hypervisor-provider -t ${CLOUD_HYPERVISOR_PROVIDER_IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${CLOUD_HYPERVISOR_PROVIDER_IMG}

##@ Deployment

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${CLOUD_HYPERVISOR_PROVIDER_IMG}
	kubectl apply -k config/default

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	kubectl delete -k config/default


##@ Tools

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
OPENAPI_EXTRACTOR ?= $(LOCALBIN)/openapi-extractor
OPENAPI_GEN ?= $(LOCALBIN)/openapi-gen
VGOPATH ?= $(LOCALBIN)/vgopath
GEN_CRD_API_REFERENCE_DOCS ?= $(LOCALBIN)/gen-crd-api-reference-docs
ADDLICENSE ?= $(LOCALBIN)/addlicense
PROTOC_GEN_GOGO ?= $(LOCALBIN)/protoc-gen-gogo
MODELS_SCHEMA ?= $(LOCALBIN)/models-schema
GOIMPORTS ?= $(LOCALBIN)/goimports
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint

## Tool Versions
KUSTOMIZE_VERSION ?= v5.6.0
VGOPATH_VERSION ?= v0.1.3
CONTROLLER_TOOLS_VERSION ?= v0.16.0
GEN_CRD_API_REFERENCE_DOCS_VERSION ?= v0.3.0
ADDLICENSE_VERSION ?= v1.1.1
PROTOC_GEN_GOGO_VERSION ?= v1.3.2
GOIMPORTS_VERSION ?= v0.26.0
GOLANGCI_LINT_VERSION ?= v1.64.6
OPENAPI_EXTRACTOR_VERSION ?= v0.1.9
SETUP_ENVTEST_VERSION ?= release-0.19

KUSTOMIZE_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	@if test -x $(LOCALBIN)/kustomize && ! $(LOCALBIN)/kustomize version | grep -q $(KUSTOMIZE_VERSION); then \
		echo "$(LOCALBIN)/kustomize version is not expected $(KUSTOMIZE_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/kustomize; \
	fi
	test -s $(LOCALBIN)/kustomize || { curl -Ss $(KUSTOMIZE_INSTALL_SCRIPT) | bash -s -- $(subst v,,$(KUSTOMIZE_VERSION)) $(LOCALBIN); }

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && $(LOCALBIN)/controller-gen --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)


.PHONY: openapi-gen
openapi-gen: $(OPENAPI_GEN) ## Download openapi-gen locally if necessary.
$(OPENAPI_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/openapi-gen || GOBIN=$(LOCALBIN) go install k8s.io/kube-openapi/cmd/openapi-gen


.PHONY: vgopath
vgopath: $(VGOPATH) ## Download vgopath locally if necessary.
.PHONY: $(VGOPATH)
$(VGOPATH): $(LOCALBIN)
	@if test -x $(LOCALBIN)/vgopath && ! $(LOCALBIN)/vgopath version | grep -q $(VGOPATH_VERSION); then \
		echo "$(LOCALBIN)/vgopath version is not expected $(VGOPATH_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/vgopath; \
	fi
	test -s $(LOCALBIN)/vgopath || GOBIN=$(LOCALBIN) go install github.com/ironcore-dev/vgopath@$(VGOPATH_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION)

.PHONY: openapi-extractor
openapi-extractor: $(OPENAPI_EXTRACTOR) ## Download openapi-extractor locally if necessary.
$(OPENAPI_EXTRACTOR): $(LOCALBIN)
	test -s $(LOCALBIN)/openapi-extractor || GOBIN=$(LOCALBIN) go install github.com/ironcore-dev/openapi-extractor/cmd/openapi-extractor@$(OPENAPI_EXTRACTOR_VERSION)

.PHONY: gen-crd-api-reference-docs
gen-crd-api-reference-docs: $(GEN_CRD_API_REFERENCE_DOCS) ## Download gen-crd-api-reference-docs locally if necessary.
$(GEN_CRD_API_REFERENCE_DOCS): $(LOCALBIN)
	test -s $(LOCALBIN)/gen-crd-api-reference-docs || GOBIN=$(LOCALBIN) go install github.com/ahmetb/gen-crd-api-reference-docs@$(GEN_CRD_API_REFERENCE_DOCS_VERSION)

.PHONY: addlicense
addlicense: $(ADDLICENSE) ## Download addlicense locally if necessary.
$(ADDLICENSE): $(LOCALBIN)
	test -s $(LOCALBIN)/addlicense || GOBIN=$(LOCALBIN) go install github.com/google/addlicense@$(ADDLICENSE_VERSION)

.PHONY: protoc-gen-gogo
protoc-gen-gogo: $(PROTOC_GEN_GOGO) ## Download protoc-gen-gogo locally if necessary.
$(PROTOC_GEN_GOGO): $(LOCALBIN)
	test -s $(LOCALBIN)/protoc-gen-gogo || GOBIN=$(LOCALBIN) go install github.com/gogo/protobuf/protoc-gen-gogo@$(PROTOC_GEN_GOGO_VERSION)

.PHONY: models-schema
models-schema: $(MODELS_SCHEMA) ## Install models-schema locally if necessary.
$(MODELS_SCHEMA): $(LOCALBIN)
	test -s $(LOCALBIN)/models-schema || GOBIN=$(LOCALBIN) go install github.com/ironcore-dev/ironcore/models-schema

.PHONY: goimports
goimports: $(GOIMPORTS) ## Download goimports locally if necessary.
$(GOIMPORTS): $(LOCALBIN)
	test -s $(LOCALBIN)/goimports || GOBIN=$(LOCALBIN) go install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	test -s $(LOCALBIN)/golangci-lint || GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)