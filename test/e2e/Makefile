.PHONY: default
default: test

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } END { printf "\n" }' $(MAKEFILE_LIST)

################################################################################
# Variables
################################################################################

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

# Inherit from parent or set defaults
REGISTRY ?= registry.nordix.org/cloud-native/meridio-2
VERSION ?= latest
KUBECTL ?= kubectl
KIND ?= kind

# E2E specific
CLUSTER_NAME ?= kind
KUBERNETES_WORKERS ?= 4
BREAKOUT_NETWORK ?= kind-breakout

# Tool versions
MULTUS_VERSION ?= v4.1.2
WHEREABOUTS_VERSION ?= v0.8.0

# Suites
SUITES ?= common-appnetwork separate-appnetwork

# Test flags (e.g., make test GINKGO_FLAGS="--junit-report=report.xml")
GINKGO_FLAGS ?=

# Paths
PROJECT_ROOT := $(shell cd ../.. && pwd)
LOCALBIN ?= $(PROJECT_ROOT)/bin
KUSTOMIZE ?= $(LOCALBIN)/kustomize

################################################################################
##@ Cluster setup
################################################################################

.PHONY: cluster
cluster: kustomize multus cni-plugins whereabouts cert-manager gateway-api kind-network vpn-gateway rp-filter ## Setup cluster dependencies.

.PHONY: kustomize
kustomize: ## Install kustomize from parent Makefile.
	$(MAKE) -C $(PROJECT_ROOT) kustomize

.PHONY: multus
multus: ## Install Multus CNI.
	@$(KUBECTL) get daemonset -n kube-system kube-multus-ds >/dev/null 2>&1 || { \
		$(KUBECTL) apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/$(MULTUS_VERSION)/deployments/multus-daemonset.yml; \
		$(KUBECTL) rollout status daemonset kube-multus-ds -n kube-system --timeout=300s; \
	}

.PHONY: cni-plugins
cni-plugins: ## Install CNI plugins.
	@$(KUBECTL) get daemonset -n kube-system install-cni-plugins >/dev/null 2>&1 || { \
		curl -sL https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/master/e2e/templates/cni-install.yml.j2 | \
		sed '/^\s*wget / s/$$/ || { echo "wget failed"; exit 1; }/' | \
		$(KUBECTL) apply -f -; \
		$(KUBECTL) rollout status daemonset install-cni-plugins -n kube-system --timeout=300s; \
	}

.PHONY: whereabouts
whereabouts: ## Install Whereabouts IPAM.
	@$(KUBECTL) get daemonset -n kube-system whereabouts >/dev/null 2>&1 || { \
		$(KUBECTL) apply \
		-f https://raw.githubusercontent.com/k8snetworkplumbingwg/whereabouts/$(WHEREABOUTS_VERSION)/doc/crds/daemonset-install.yaml \
		-f https://raw.githubusercontent.com/k8snetworkplumbingwg/whereabouts/$(WHEREABOUTS_VERSION)/doc/crds/whereabouts.cni.cncf.io_ippools.yaml \
		-f https://raw.githubusercontent.com/k8snetworkplumbingwg/whereabouts/$(WHEREABOUTS_VERSION)/doc/crds/whereabouts.cni.cncf.io_overlappingrangeipreservations.yaml; \
		$(KUBECTL) rollout status daemonset whereabouts -n kube-system --timeout=300s; \
	}

.PHONY: gateway-api
gateway-api: ## Install Gateway API CRDs.
	@$(KUBECTL) get crd gateways.gateway.networking.k8s.io >/dev/null 2>&1 || \
		$(KUBECTL) apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.0/standard-install.yaml

.PHONY: cert-manager
cert-manager: ## Install cert-manager.
	@$(KUBECTL) get deployment -n cert-manager cert-manager >/dev/null 2>&1 || { \
		$(KUBECTL) apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml; \
		$(KUBECTL) wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager; \
		$(KUBECTL) wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager-webhook; \
		$(KUBECTL) wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager-cainjector; \
	}

.PHONY: kind-network
kind-network: ## Setup kind bridge network.
	@docker network inspect $(BREAKOUT_NETWORK) >/dev/null 2>&1 || { \
		docker network create $(BREAKOUT_NETWORK) --driver=bridge \
			--subnet=172.19.0.0/16 \
			--gateway=172.19.0.1 \
			--ipv6 --subnet=fc00:dead:beef::/48 \
			--opt com.docker.network.bridge.name=br-meridio; \
		for i in $$(seq 0 $(KUBERNETES_WORKERS)); do \
			node=$$($(KIND) get nodes --name $(CLUSTER_NAME) | sed -n "$$((i+1))p"); \
			[ -n "$$node" ] && docker network connect $(BREAKOUT_NETWORK) $$node || true; \
		done; \
	}

.PHONY: vpn-gateway
vpn-gateway: ## Deploy VPN gateway container.
	@docker ps --filter name=vpn-gateway --format '{{.Names}}' | grep -q vpn-gateway || { \
		cd $(PROJECT_ROOT)/hack/vpn-gateway && \
		docker compose build && \
		docker compose up -d; \
	}

.PHONY: rp-filter
rp-filter: ## Configure rp_filter=2 on all Kind nodes.
	@echo "Configuring rp_filter=2 on all nodes..."; \
	for node in $$($(KIND) get nodes --name $(CLUSTER_NAME)); do \
		docker exec $$node sysctl -w net.ipv4.conf.all.rp_filter=2; \
		docker exec $$node sysctl -w net.ipv4.conf.default.rp_filter=2; \
	done

.PHONY: cluster-cleanup
cluster-cleanup: ## Cleanup cluster dependencies.
	cd $(PROJECT_ROOT)/hack/vpn-gateway && docker compose down 2>/dev/null || true
	docker network rm $(BREAKOUT_NETWORK) 2>/dev/null || true

################################################################################
# Helper functions
################################################################################

# deploy-suite: Deploy a test suite
# Args: $(1)=namespace, $(2)=suite-dir, $(3)=suite-label, $(4)=sllb-deployments
define deploy-suite
	@NS=$(1); \
	SUITE_DIR=$(2); \
	SUITE_LABEL=$(3); \
	$(KUBECTL) create namespace $$NS --dry-run=client -o yaml | $(KUBECTL) apply -f -; \
	$(KUBECTL) label namespace $$NS e2e-suite=$$SUITE_LABEL --overwrite; \
	$(KUBECTL) apply -f suites/common/gatewayclass.yaml; \
	sed 's|registry.nordix.org/cloud-native/meridio-2|$(REGISTRY)|g; s|:latest|:$(VERSION)|g' \
		$(PROJECT_ROOT)/config/templates/lb-deployment.yaml | \
		$(KUBECTL) create configmap lb-template-override-$$SUITE_LABEL --from-file=lb-deployment.yaml=/dev/stdin -n $$NS --dry-run=client -o yaml | \
		$(KUBECTL) apply -f -; \
	cd $$SUITE_DIR && $(KUSTOMIZE) edit set image controller-manager=$(REGISTRY)/controller-manager:$(VERSION); \
	cd $$SUITE_DIR && $(KUSTOMIZE) build . | $(KUBECTL) apply -f -; \
	$(KUBECTL) set env deployment/meridio-2-controller-manager-$$SUITE_LABEL -n $$NS MERIDIO_TEMPLATE_PATH=/templates-override; \
	if ! $(KUBECTL) get deployment meridio-2-controller-manager-$$SUITE_LABEL -n $$NS -o jsonpath='{.spec.template.spec.volumes[*].name}' | grep -q templates-override; then \
		$(KUBECTL) patch deployment meridio-2-controller-manager-$$SUITE_LABEL -n $$NS --type=json -p='[{"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"templates-override","mountPath":"/templates-override"}},{"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"templates-override","configMap":{"name":"lb-template-override-'$$SUITE_LABEL'"}}}]'; \
	fi; \
	$(KUBECTL) wait --for=condition=Available --timeout=180s -n $$NS deployment/meridio-2-controller-manager-$$SUITE_LABEL; \
	$(KUBECTL) wait --for=condition=Ready --timeout=60s -n $$NS certificate/meridio-2-serving-cert-$$SUITE_LABEL; \
	$(KUBECTL) apply -f $$SUITE_DIR/rbac.yaml; \
	$(KUBECTL) apply -f $$SUITE_DIR/nad.yaml -n $$NS; \
	$(KUBECTL) apply -f $$SUITE_DIR/gateway.yaml -n $$NS; \
	$(KUBECTL) apply -f $$SUITE_DIR/dg.yaml -n $$NS; \
	$(KUBECTL) apply -f $$SUITE_DIR/routing.yaml -n $$NS; \
	timeout=300; elapsed=0; \
	until $(foreach dep,$(4),$(KUBECTL) get deployment $(dep) -n $$NS >/dev/null 2>&1 &&) true; do \
		sleep 1; elapsed=$$((elapsed + 1)); \
		if [ $$elapsed -ge $$timeout ]; then echo "Timeout waiting for deployments: $(4)"; exit 1; fi; \
	done; \
	$(KUBECTL) wait --for=condition=Available --timeout=300s -n $$NS $(foreach dep,$(4),deployment/$(dep)); \
	sed 's|registry.nordix.org/cloud-native/meridio-2/network-sidecar:latest|$(REGISTRY)/network-sidecar:$(VERSION)|g' $$SUITE_DIR/targets.yaml | $(KUBECTL) apply -n $$NS -f -; \
	$(KUBECTL) wait --for=condition=Available --timeout=300s -n $$NS $$($(KUBECTL) get deployment -n $$NS -l app -o name | grep target)
endef

################################################################################
##@ Suite: common-appnetwork
################################################################################

.PHONY: common-appnetwork
common-appnetwork: deploy-common-appnetwork test-common-appnetwork ## Deploy and test common-appnetwork suite.

.PHONY: deploy-common-appnetwork
deploy-common-appnetwork: cluster ## Deploy common-appnetwork topology.
	$(call deploy-suite,e2e-common-appnet,$(shell pwd)/suites/common-appnetwork,common-appnet,sllb-gw-b1 sllb-gw-b2)

.PHONY: test-common-appnetwork
test-common-appnetwork: ## Run e2e tests for common-appnetwork suite.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v --tags=e2e --focus="Common App Network" $(GINKGO_FLAGS)

.PHONY: undeploy-common-appnetwork
undeploy-common-appnetwork: ## Undeploy common-appnetwork topology.
	@NS=e2e-common-appnet; \
	$(KUBECTL) delete validatingwebhookconfiguration meridio-2-validating-webhook-configuration-common-appnet --ignore-not-found=true; \
	$(KUBECTL) delete namespace $$NS --ignore-not-found=true

################################################################################
##@ Suite: separate-appnetwork
################################################################################

.PHONY: separate-appnetwork
separate-appnetwork: deploy-separate-appnetwork test-separate-appnetwork ## Deploy and test separate-appnetwork suite.

.PHONY: deploy-separate-appnetwork
deploy-separate-appnetwork: cluster ## Deploy separate-appnetwork topology.
	$(call deploy-suite,e2e-separate-appnet,$(shell pwd)/suites/separate-appnetwork,separate-appnet,sllb-gw-a1 sllb-gw-a2)

.PHONY: test-separate-appnetwork
test-separate-appnetwork: ## Run e2e tests for separate-appnetwork suite.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v --tags=e2e --focus="Separate App Network" $(GINKGO_FLAGS)

.PHONY: undeploy-separate-appnetwork
undeploy-separate-appnetwork: ## Undeploy separate-appnetwork topology.
	@NS=e2e-separate-appnet; \
	$(KUBECTL) delete validatingwebhookconfiguration meridio-2-validating-webhook-configuration-separate-appnet --ignore-not-found=true; \
	$(KUBECTL) delete namespace $$NS --ignore-not-found=true

################################################################################
##@ All suites
################################################################################

.PHONY: deploy-all
deploy-all: ## Deploy all suites.
	@for suite in $(SUITES); do $(MAKE) deploy-$$suite || exit 1; done

.PHONY: test
test: deploy-all ## Run all e2e tests in parallel.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v -p --tags=e2e $(GINKGO_FLAGS)

.PHONY: undeploy-all
undeploy-all: ## Undeploy all suites.
	@for suite in $(SUITES); do $(MAKE) undeploy-$$suite; done
