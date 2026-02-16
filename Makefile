# Workspace Operator Makefile
# Targets: manifests, generate, test, build, docker-build, deploy, lint

# Image URL to use for building/pushing operator and gateway images.
IMG ?= workspace-operator:latest
GATEWAY_IMG ?= workspace-gateway:latest
WORKSPACE_IMG ?= workspace:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set).
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by 'make'.
SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec

# Binaries and tools.
CONTROLLER_GEN ?= $(shell which controller-gen 2>/dev/null || echo "$(GOBIN)/controller-gen")
GOLANGCI_LINT ?= $(shell which golangci-lint 2>/dev/null || echo "$(GOBIN)/golangci-lint")
KUSTOMIZE ?= $(shell which kustomize 2>/dev/null || echo "$(GOBIN)/kustomize")
ENVTEST ?= $(shell which setup-envtest 2>/dev/null || echo "$(GOBIN)/setup-envtest")

# Envtest: directory for etcd/kube-apiserver binaries; K8s version from k8s.io/api.
LOCALBIN ?= $(CURDIR)/testbin
ENVTEST_K8S_VERSION ?= $(shell go list -m -f '{{ .Version }}' k8s.io/api 2>/dev/null | awk -F'[v.]' '{printf "1.%d", $$3}')

# Default target.
all: build

##@ General
.PHONY: help
help:
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development
.PHONY: manifests
manifests: controller-gen
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases output:rbac:dir=config/rbac

.PHONY: generate
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile=hack/boilerplate.go.txt paths="./..."

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test: fmt vet setup-envtest ## Run all tests (downloads envtest binaries on first run).
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -i --bin-dir $(LOCALBIN) -p path 2>/dev/null)" go test ./... -coverprofile=cover.out

.PHONY: test-short
test-short: fmt vet ## Run tests excluding envtest integration tests (no etcd/kube-apiserver needed).
	go test -short ./... -coverprofile=cover.out

.PHONY: build
build: fmt vet
	go build -o bin/manager main.go

.PHONY: run
run: fmt vet generate
	go run ./main.go

##@ Lint
.PHONY: lint
lint: golangci-lint
	$(GOLANGCI_LINT) run ./...

##@ Docker
.PHONY: docker-build
docker-build:
	docker build -t $(IMG) -f Dockerfile.operator .
	docker build -t $(GATEWAY_IMG) -f Dockerfile.gateway .
	docker build -t $(WORKSPACE_IMG) -f Dockerfile.workspace .

##@ Deployment
.PHONY: install
install: manifests
	kubectl apply -f config/crd/bases/

.PHONY: deploy
deploy: manifests kustomize
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG)
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy:
	$(KUSTOMIZE) build config/default | kubectl delete -f -

##@ Dependencies
.PHONY: controller-gen
controller-gen:
	@if command -v $(CONTROLLER_GEN) >/dev/null 2>&1; then \
		echo "controller-gen found"; \
	else \
		echo "Installing controller-gen"; \
		go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0; \
	fi

.PHONY: kustomize
kustomize:
	@if command -v $(KUSTOMIZE) >/dev/null 2>&1; then \
		echo "kustomize found"; \
	else \
		echo "Installing kustomize"; \
		go install sigs.k8s.io/kustomize/kustomize/v5@latest; \
	fi

.PHONY: golangci-lint
golangci-lint:
	@if command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then \
		echo "golangci-lint found"; \
	else \
		echo "Installing golangci-lint"; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.55.2; \
	fi

.PHONY: envtest
envtest:
	@if command -v $(ENVTEST) >/dev/null 2>&1; then \
		echo "setup-envtest found"; \
	else \
		echo "Installing setup-envtest"; \
		go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest; \
	fi

.PHONY: setup-envtest
setup-envtest: envtest ## Download etcd and kube-apiserver binaries for envtest integration tests.
	@echo "Setting up envtest binaries for Kubernetes $(ENVTEST_K8S_VERSION)..."
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path

.PHONY: tidy
tidy:
	go mod tidy
