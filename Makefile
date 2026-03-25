.PHONY: default
default:
	$(MAKE) -s $(IMAGES)

.PHONY: all
all: default

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } END { printf "\n" }' $(MAKEFILE_LIST)

################################################################################
# Variables
################################################################################

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

IMAGES ?= controller-manager stateless-load-balancer router network-sidecar

# Versions
VERSION ?= latest
VERSION_CONTROLLER_MANAGER ?= $(VERSION)
VERSION_SLLB ?= $(VERSION)
VERSION_ROUTER ?= $(VERSION)
VERSION_NETWORK_SIDECAR ?= $(VERSION)
LOCAL_VERSION ?= $(VERSION)

# Container registry
REGISTRY ?= localhost:5001
# REGISTRY ?= registry.nordix.org/cloud-native/meridio-2

# Namespace to deploy into
NAMESPACE ?= meridio-2

# Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

# Tools
KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

# Tool versions
KUSTOMIZE_VERSION ?= v5.7.1
CONTROLLER_TOOLS_VERSION ?= v0.20.0
GOLANGCI_LINT_VERSION ?= v2.7.2
CERT_MANAGER_VERSION ?= v1.16.2

# Build
BUILD_DIR ?= build
BUILD_STEPS ?= build tag push
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')

################################################################################
# Image: Build, tag, push
################################################################################

.PHONY: build
build:
	docker build -t $(IMAGE):$(LOCAL_VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f ./$(BUILD_DIR)/$(IMAGE)/Dockerfile .

.PHONY: tag
tag:
	docker tag $(IMAGE):$(LOCAL_VERSION) $(REGISTRY)/$(IMAGE):$(VERSION)

.PHONY: push
push:
	docker push $(REGISTRY)/$(IMAGE):$(VERSION)

################################################################################
##@ Components (Build, tag, push): Use VERSION to set the version. Use BUILD_STEPS to set the build steps (build, tag, push).
################################################################################

.PHONY: controller-manager
controller-manager: ## Build the controller-manager.
	VERSION=$(VERSION_CONTROLLER_MANAGER) IMAGE=controller-manager $(MAKE) -s $(BUILD_STEPS)

.PHONY: stateless-load-balancer
stateless-load-balancer: ## Build the stateless-load-balancer.
	VERSION=$(VERSION_SLLB) IMAGE=stateless-load-balancer $(MAKE) -s $(BUILD_STEPS)

.PHONY: router
router: ## Build the router.
	VERSION=$(VERSION_ROUTER) IMAGE=router $(MAKE) -s $(BUILD_STEPS)

.PHONY: network-sidecar
network-sidecar: ## Build the network-sidecar.
	VERSION=$(VERSION_NETWORK_SIDECAR) IMAGE=network-sidecar $(MAKE) -s $(BUILD_STEPS)

################################################################################
##@ Examples
################################################################################

.PHONY: example-target
example-target: ## Build the example target application.
	BUILD_DIR=examples/target/build IMAGE=example-target $(MAKE) -s $(BUILD_STEPS)

################################################################################
##@ Testing & Code check
################################################################################

.PHONY: check
check: lint fmt vet test ## Check everything.

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter.
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes.
	"$(GOLANGCI_LINT)" run --fix

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: generate setup-envtest ## Run the unit tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

.PHONY: install-hooks
install-hooks: ## Install git pre-commit hook to run 'make check' before commits.
	@echo "Installing git pre-commit hook..."
	@cp scripts/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "   Pre-commit hook installed. Run 'make check' will now run automatically before commits."
	@echo "   To skip the check, use: git commit --no-verify"

################################################################################
##@ Code generation
################################################################################

.PHONY: generate
generate: manifests generate-controller ## Generate everything.

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate-controller
generate-controller: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."

################################################################################
##@ Deployment
################################################################################

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize cert-manager gateway-api ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/controller-manager && "$(KUSTOMIZE)" edit set image controller-manager=$(REGISTRY)/controller-manager:$(VERSION_CONTROLLER_MANAGER)
	cd config/default && "$(KUSTOMIZE)" edit set namespace $(NAMESPACE)
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	cd config/default && "$(KUSTOMIZE)" edit set namespace $(NAMESPACE)
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/controller-manager && "$(KUSTOMIZE)" edit set image controller-manager=$(REGISTRY)/controller-manager:$(VERSION_CONTROLLER_MANAGER)
	cd config/default && "$(KUSTOMIZE)" edit set namespace $(NAMESPACE)
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

################################################################################
# Tools
################################################################################

#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

.PHONY: kustomize
kustomize: $(KUSTOMIZE) # Download kustomize locally if necessary
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) # Download controller-gen locally if necessary
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest # Download the binaries required for ENVTEST in the local bin directory
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) # Download setup-envtest locally if necessary
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) # Download golangci-lint locally if necessary
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: cert-manager
cert-manager: # Install cert-manager if not present
	@kubectl get deployment -n cert-manager cert-manager >/dev/null 2>&1 || { \
		echo "Installing cert-manager..."; \
		kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/$(CERT_MANAGER_VERSION)/cert-manager.yaml; \
		kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager; \
		kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager-webhook; \
		kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager-cainjector; \
	}

.PHONY: gateway-api
gateway-api: # Install Gateway API CRDs if not present
	@kubectl get crd gateways.gateway.networking.k8s.io >/dev/null 2>&1 || { \
		echo "Installing Gateway API CRDs..."; \
		kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.0/standard-install.yaml; \
	}

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
