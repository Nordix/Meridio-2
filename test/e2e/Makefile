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
SUITES_IPV4 ?= shared-appnetwork separate-appnetwork separate-static-appnetwork sctp-multihoming ipv4-simple pod-cache-label
SUITES_DS ?= dual-stack-simple

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
		docker exec $$node sh -c ' \
			sysctl -w net.ipv4.conf.all.rp_filter=2 && \
			sysctl -w net.ipv4.conf.default.rp_filter=2 && \
			echo "net.ipv4.conf.all.rp_filter=2" >> /etc/sysctl.conf && \
			echo "net.ipv4.conf.default.rp_filter=2" >> /etc/sysctl.conf \
		'; \
	done

.PHONY: cluster-cleanup
cluster-cleanup: ## Cleanup cluster dependencies.
	cd $(PROJECT_ROOT)/hack/vpn-gateway && docker compose down 2>/dev/null || true
	docker network rm $(BREAKOUT_NETWORK) 2>/dev/null || true

################################################################################
# Helper functions
################################################################################

# deploy-suite: Deploy a test suite
# Args: $(1)=namespace, $(2)=suite-dir, $(3)=suite-label, $(4)=sllbr-deployments, $(5)=lb-template (optional)
define deploy-suite
	@NS=$(1); \
	SUITE_DIR=$(2); \
	SUITE_LABEL=$(3); \
	LB_TEMPLATE=$(if $(5),$(5),$(PROJECT_ROOT)/config/templates/lb-deployment.yaml); \
	$(KUBECTL) create namespace $$NS --dry-run=client -o yaml | $(KUBECTL) apply -f -; \
	$(KUBECTL) label namespace $$NS e2e-suite=$$SUITE_LABEL --overwrite; \
	$(KUBECTL) apply -f suites/common/gatewayclass.yaml; \
	sed 's|registry.nordix.org/cloud-native/meridio-2|$(REGISTRY)|g; s|:latest|:$(VERSION)|g' \
		$$LB_TEMPLATE | \
		$(KUBECTL) create configmap lb-template-override-$$SUITE_LABEL --from-file=lb-deployment.yaml=/dev/stdin -n $$NS --dry-run=client -o yaml | \
		$(KUBECTL) apply -f -; \
	cd $$SUITE_DIR && $(KUSTOMIZE) edit set image controller-manager=$(REGISTRY)/controller-manager:$(VERSION); \
	cd $$SUITE_DIR && $(KUSTOMIZE) build . | $(KUBECTL) apply -f -; \
	$(KUBECTL) set env deployment/meridio-2-controller-manager-$$SUITE_LABEL -n $$NS MERIDIO_TEMPLATE_PATH=/templates-override; \
	if ! $(KUBECTL) get deployment meridio-2-controller-manager-$$SUITE_LABEL -n $$NS -o jsonpath='{.spec.template.spec.volumes[*].name}' | grep -q templates-override; then \
		$(KUBECTL) patch deployment meridio-2-controller-manager-$$SUITE_LABEL -n $$NS --type=json -p='[{"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"templates-override","mountPath":"/templates-override"}},{"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"templates-override","configMap":{"name":"lb-template-override-'$$SUITE_LABEL'"}}}]'; \
	fi; \
	$(KUBECTL) wait --for=condition=Available --timeout=600s -n $$NS deployment/meridio-2-controller-manager-$$SUITE_LABEL; \
	$(KUBECTL) wait --for=condition=Ready --timeout=60s -n $$NS certificate/meridio-2-serving-cert-$$SUITE_LABEL; \
	timeout=60; elapsed=0; \
	until $(KUBECTL) get endpoints -n $$NS meridio-2-webhook-service-$$SUITE_LABEL -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q .; do \
		sleep 1; elapsed=$$((elapsed + 1)); \
		if [ $$elapsed -ge $$timeout ]; then echo "Timeout waiting for webhook endpoint"; exit 1; fi; \
	done; \
	timeout=30; elapsed=0; \
	until $(KUBECTL) apply --dry-run=server -n $$NS -f - >/dev/null 2>&1 <<< '{"apiVersion":"meridio-2.nordix.org/v1alpha1","kind":"L34Route","metadata":{"name":"webhook-probe"},"spec":{"parentRefs":[{"name":"probe"}],"destinationCIDRs":["0.0.0.0/32"],"protocols":["TCP"],"destinationPorts":["1"],"priority":1}}'; do \
		sleep 1; elapsed=$$((elapsed + 1)); \
		if [ $$elapsed -ge $$timeout ]; then echo "Timeout waiting for webhook to be ready"; exit 1; fi; \
	done; \
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
	$(KUBECTL) wait --for=condition=Available --timeout=600s -n $$NS $(foreach dep,$(4),deployment/$(dep)); \
	sed 's|registry.nordix.org/cloud-native/meridio-2/network-sidecar:latest|$(REGISTRY)/network-sidecar:$(VERSION)|g; s|registry.nordix.org/cloud-native/meridio-2/example-target:latest|$(REGISTRY)/example-target:$(VERSION)|g' $$SUITE_DIR/targets.yaml | $(KUBECTL) apply -n $$NS -f -; \
	$(KUBECTL) wait --for=condition=Available --timeout=600s -n $$NS $$($(KUBECTL) get deployment -n $$NS -l app -o name | grep target); \
	echo "Waiting for all pods to be ready in namespace $$NS..."; \
	timeout=300; elapsed=0; \
	until [ $$($(KUBECTL) get pods -n $$NS --field-selector=status.phase!=Succeeded,status.phase!=Failed -o json | jq -r '.items[] | select(.status.phase=="Running" and ([.status.conditions[] | select(.type=="Ready" and .status=="True")] | length) > 0) | .metadata.name' | wc -l) -eq $$($(KUBECTL) get pods -n $$NS --field-selector=status.phase!=Succeeded,status.phase!=Failed --no-headers | wc -l) ]; do \
		sleep 2; elapsed=$$((elapsed + 2)); \
		if [ $$elapsed -ge $$timeout ]; then \
			echo "Timeout waiting for all pods to be ready. Current state:"; \
			$(KUBECTL) get pods -n $$NS; \
			exit 1; \
		fi; \
	done; \
	echo "All pods ready in namespace $$NS"
endef

#################################################################################
##@ Topology: ipv4-simple
################################################################################

.PHONY: deploy-ipv4-simple
deploy-ipv4-simple: cluster ## Deploy ipv4-simple topology.
	$(call deploy-suite,e2e-ipv4-simple,$(shell pwd)/suites/ipv4-simple,ipv4-simple,sllbr-gw-m1)

.PHONY: undeploy-ipv4-simple
undeploy-ipv4-simple: ## Undeploy ipv4-simple topology.
	@NS=e2e-ipv4-simple; \
	$(KUBECTL) delete validatingwebhookconfiguration meridio-2-validating-webhook-configuration-ipv4-simple --ignore-not-found=true; \
	$(KUBECTL) delete namespace $$NS --ignore-not-found=true

#################################################################################
##@ Suite: separate-appnetwork
################################################################################

.PHONY: separate-appnetwork
separate-appnetwork: deploy-separate-appnetwork test-separate-appnetwork ## Deploy and test separate-appnetwork suite.

.PHONY: deploy-separate-appnetwork
deploy-separate-appnetwork: cluster ## Deploy separate-appnetwork topology.
	$(call deploy-suite,e2e-separate-appnetwork,$(shell pwd)/suites/separate-appnetwork,separate-appnet,sllbr-gw-a1 sllbr-gw-a2)

.PHONY: test-separate-appnetwork
test-separate-appnetwork: ## Run e2e tests for separate-appnetwork suite.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v --tags=e2e --focus="Separate App Network" $(GINKGO_FLAGS)

.PHONY: undeploy-separate-appnetwork
undeploy-separate-appnetwork: ## Undeploy separate-appnetwork topology.
	@NS=e2e-separate-appnetwork; \
	$(KUBECTL) delete validatingwebhookconfiguration meridio-2-validating-webhook-configuration-separate-appnet --ignore-not-found=true; \
	$(KUBECTL) delete namespace $$NS --ignore-not-found=true

##############################################################################
##@ Suite: separate-static-appnetwork
################################################################################

.PHONY: separate-static-appnetwork
separate-static-appnetwork: deploy-separate-static-appnetwork test-separate-static-appnetwork ## Deploy and test separate-static-appnetwork suite.

.PHONY: deploy-separate-static-appnetwork
deploy-separate-static-appnetwork: cluster ## Deploy separate-static-appnetwork topology.
	$(call deploy-suite,e2e-separate-static-appnetwork,$(shell pwd)/suites/separate-static-appnetwork,sep-static-net,sllbr-gw-a1 sllbr-gw-a2)

.PHONY: test-separate-static-appnetwork
test-separate-static-appnetwork: ## Run e2e tests for separate-static-appnetwork suite.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v --tags=e2e --focus="Separate Static App Network" $(GINKGO_FLAGS)

.PHONY: undeploy-separate-static-appnetwork
undeploy-separate-static-appnetwork: ## Undeploy separate-static-appnetwork topology.
	@NS=e2e-separate-static-appnetwork; \
	$(KUBECTL) delete validatingwebhookconfiguration meridio-2-validating-webhook-configuration-sep-static-net --ignore-not-found=true; \
	$(KUBECTL) delete namespace $$NS --ignore-not-found=true

##############################################################################
##@ Suite: shared-appnetwork
################################################################################

.PHONY: shared-appnetwork
shared-appnetwork: deploy-shared-appnetwork test-shared-appnetwork ## Deploy and test shared-appnetwork suite.

.PHONY: deploy-shared-appnetwork
deploy-shared-appnetwork: cluster ## Deploy shared-appnetwork topology.
	$(call deploy-suite,e2e-shared-appnetwork,$(shell pwd)/suites/shared-appnetwork,shared-appnet,sllbr-gw-b1 sllbr-gw-b2)

.PHONY: test-shared-appnetwork
test-shared-appnetwork: ## Run e2e tests for shared-appnetwork suite.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v --tags=e2e --focus="Shared App Network" $(GINKGO_FLAGS)

.PHONY: undeploy-shared-appnetwork
undeploy-shared-appnetwork: ## Undeploy shared-appnetwork topology.
	@NS=e2e-shared-appnetwork; \
	$(KUBECTL) delete validatingwebhookconfiguration meridio-2-validating-webhook-configuration-shared-appnet --ignore-not-found=true; \
	$(KUBECTL) delete namespace $$NS --ignore-not-found=true

#################################################################################
##@ Suite: sctp-multihoming
################################################################################

.PHONY: sctp-multihoming
sctp-multihoming: deploy-sctp-multihoming test-sctp-multihoming ## Deploy and test sctp-multihoming suite.

.PHONY: deploy-sctp-multihoming
deploy-sctp-multihoming: cluster ## Deploy sctp-multihoming topology.
	$(call deploy-suite,e2e-sctp-multihoming,$(shell pwd)/suites/sctp-multihoming,sctp-multihoming,sllbr-sctp-gw1 sllbr-sctp-gw2)

.PHONY: test-sctp-multihoming
test-sctp-multihoming: ## Run e2e tests for sctp-multihoming suite.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v --tags=e2e --focus="SCTP Multihoming" $(GINKGO_FLAGS)

.PHONY: undeploy-sctp-multihoming
undeploy-sctp-multihoming: ## Undeploy sctp-multihoming topology.
	@NS=e2e-sctp-multihoming; \
	$(KUBECTL) delete validatingwebhookconfiguration meridio-2-validating-webhook-configuration-sctp-multihoming --ignore-not-found=true; \
	$(KUBECTL) delete namespace $$NS --ignore-not-found=true

###############################################################################
##@ Suite: low-mtu
################################################################################

.PHONY: low-mtu
low-mtu: deploy-low-mtu test-low-mtu ## Deploy and test low-mtu suite.

.PHONY: deploy-low-mtu
deploy-low-mtu: deploy-ipv4-simple ## Deploy ipv4-simple topology.

.PHONY: test-low-mtu
test-low-mtu: ## Run PMTU discovery tests.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v --tags=e2e --focus="Low MTU" $(GINKGO_FLAGS)

.PHONY: undeploy-low-mtu
undeploy-low-mtu: undeploy-ipv4-simple ## Undeploy ipv4-simple topology.

################################################################################
##@ Suite: resiliency
################################################################################

.PHONY: resiliency
resiliency: deploy-resiliency test-resiliency ## Deploy and test resiliency suite.

.PHONY: deploy-resiliency
deploy-resiliency: deploy-ipv4-simple ## Deploy ipv4-simple topology.

.PHONY: test-resiliency
test-resiliency: ## Run process recovery tests.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v --tags=e2e --focus="Resiliency" $(GINKGO_FLAGS)

.PHONY: undeploy-resiliency
undeploy-resiliency: undeploy-ipv4-simple ## Undeploy ipv4-simple topology.

################################################################################
##@ Suite: pod-cache-label
################################################################################

.PHONY: pod-cache-label
pod-cache-label: deploy-pod-cache-label test-pod-cache-label ## Deploy and test pod-cache-label suite.

.PHONY: deploy-pod-cache-label
deploy-pod-cache-label: cluster ## Deploy pod-cache-label topology.
	$(call deploy-suite,e2e-pod-cache-label,$(shell pwd)/suites/pod-cache-label,pod-cache-label,sllbr-gw-pcl)

.PHONY: test-pod-cache-label
test-pod-cache-label: ## Run e2e tests for pod-cache-label suite.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v --tags=e2e --focus="Pod Cache Label" $(GINKGO_FLAGS)

.PHONY: undeploy-pod-cache-label
undeploy-pod-cache-label: ## Undeploy pod-cache-label topology.
	@NS=e2e-pod-cache-label; \
	$(KUBECTL) delete validatingwebhookconfiguration meridio-2-validating-webhook-configuration-pod-cache-label --ignore-not-found=true; \
	$(KUBECTL) delete namespace $$NS --ignore-not-found=true

#################################################################################
##@ Suite: dual-stack
################################################################################

.PHONY: dual-stack-simple
dual-stack-simple: deploy-dual-stack-simple test-dual-stack-simple ## Deploy and test dual-stack suite.

.PHONY: deploy-dual-stack-simple
deploy-dual-stack-simple: cluster ## Deploy dual-stack topology.
	$(call deploy-suite,e2e-dual-stack,$(shell pwd)/suites/dual-stack,dual-stack,sllbr-gw-ds)

.PHONY: test-dual-stack-simple
test-dual-stack-simple: ## Run e2e tests for dual-stack suite.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v --tags=e2e --focus="Dual Stack" $(GINKGO_FLAGS)

.PHONY: undeploy-dual-stack-simple
undeploy-dual-stack-simple: ## Undeploy dual-stack topology.
	@NS=e2e-dual-stack; \
	$(KUBECTL) delete validatingwebhookconfiguration meridio-2-validating-webhook-configuration-dual-stack --ignore-not-found=true; \
	$(KUBECTL) delete namespace $$NS --ignore-not-found=true

################################################################################
##@ Suite: readiness-gates
################################################################################

.PHONY: readiness-gates
readiness-gates: deploy-readiness-gates test-readiness-gates ## Deploy and test readiness-gates suite.

.PHONY: deploy-readiness-gates
deploy-readiness-gates: deploy-dual-stack-simple ## Deploy readiness-gates topology (reuses dual-stack-simple).

.PHONY: test-readiness-gates
test-readiness-gates: ## Run e2e readiness gates tests.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v --tags=e2e --focus="Readiness Gates" $(GINKGO_FLAGS)

.PHONY: undeploy-readiness-gates
undeploy-readiness-gates: undeploy-dual-stack-simple ## Undeploy readiness-gates topology (same as dual-stack-simple).

###############################################################################
##@ All ipv4 suites
################################################################################

.PHONY: ipv4
ipv4: deploy-ipv4 test-ipv4 ## Deploy and test all ipv4 suites.

.PHONY: deploy-ipv4
deploy-ipv4: ## Deploy all IPv4 suites.
	@for suite in $(SUITES_IPV4); do $(MAKE) deploy-$$suite || exit 1; done

.PHONY: test-ipv4
test-ipv4: ## Run all IPv4 e2e tests in parallel.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v -p --tags=e2e --label-filter=ipv4 $(GINKGO_FLAGS)

.PHONY: undeploy-ipv4
undeploy-ipv4: ## Undeploy all IPv4 suites.
	@for suite in $(SUITES_IPV4); do $(MAKE) undeploy-$$suite; done

################################################################################
##@ All dual-stack suites
################################################################################

.PHONY: dual-stack
dual-stack: deploy-dual-stack test-dual-stack ## Deploy and test all dual-stack suites.

.PHONY: deploy-dual-stack
deploy-dual-stack: ## Deploy all dual-stack suites.
	@for suite in $(SUITES_DS); do $(MAKE) deploy-$$suite || exit 1; done

.PHONY: test-dual-stack
test-dual-stack: ## Run all dual-stack e2e tests in parallel.
	cd $(PROJECT_ROOT)/test/e2e && go run github.com/onsi/ginkgo/v2/ginkgo -v -p --tags=e2e --label-filter=dual-stack $(GINKGO_FLAGS)

.PHONY: undeploy-dual-stack
undeploy-dual-stack: ## Undeploy all dual-stack suites.
	@for suite in $(SUITES_DS); do $(MAKE) undeploy-$$suite; done

################################################################################
##@ Suite: openshift-crc
################################################################################

# OpenShift CRC registry configuration.
# OCP_REGISTRY_HOST: external route hostname for docker push (from host).
# OCP_NAMESPACE: namespace where images and workloads are deployed.
# Derived:
#   OCP_PUSH_REGISTRY: used for docker tag/push (external route).
#   OCP_INTERNAL_REGISTRY: used by pods to pull images (cluster-internal service).
OCP_REGISTRY_HOST ?= default-route-openshift-image-registry.apps-crc.testing
OCP_NAMESPACE ?= meridio-2
OCP_PUSH_REGISTRY = $(OCP_REGISTRY_HOST)/$(OCP_NAMESPACE)
OCP_INTERNAL_REGISTRY = image-registry.openshift-image-registry.svc:5000/$(OCP_NAMESPACE)
OCP_IMAGES ?= controller-manager stateless-load-balancer router network-sidecar example-target vpn-gateway

.PHONY: crc-registry-login
crc-registry-login: ## [CRC-only] Trust CRC registry CA and docker login (needs sudo).
	@echo "Extracting CRC registry CA..."
	@$(KUBECTL) get secret -n openshift-ingress-operator router-ca -o jsonpath='{.data.tls\.crt}' | base64 -d > /tmp/crc-ca.crt
	@$(KUBECTL) get secret -n openshift-ingress router-certs-default -o jsonpath='{.data.tls\.crt}' | base64 -d >> /tmp/crc-ca.crt
	@echo "Installing CA into /etc/docker/certs.d/$(OCP_REGISTRY_HOST) (requires sudo)..."
	@sudo mkdir -p /etc/docker/certs.d/$(OCP_REGISTRY_HOST)
	@sudo cp /tmp/crc-ca.crt /etc/docker/certs.d/$(OCP_REGISTRY_HOST)/ca.crt
	@echo "Logging in to $(OCP_REGISTRY_HOST)..."
	@docker login -u kubeadmin -p "$$($(KUBECTL) whoami -t)" $(OCP_REGISTRY_HOST)
	@echo "Registry login complete"

.PHONY: push-images-openshift-crc
push-images-openshift-crc: ## Tag and push all images to CRC internal registry (requires docker login).
	@echo "Ensuring namespace $(OCP_NAMESPACE) exists..."
	@$(KUBECTL) create namespace $(OCP_NAMESPACE) --dry-run=client -o yaml | $(KUBECTL) apply -f -
	@echo "Creating ImageStreams in namespace $(OCP_NAMESPACE)..."
	@for img in $(OCP_IMAGES); do \
		$(KUBECTL) create imagestream $$img -n $(OCP_NAMESPACE) 2>/dev/null || true; \
	done
	@echo "Building VPN gateway (not covered by root Makefile)..."
	@docker build -t vpn-gateway:$(VERSION) $(PROJECT_ROOT)/hack/vpn-gateway/
	@echo "Tagging and pushing images to $(OCP_PUSH_REGISTRY)..."
	@for img in $(OCP_IMAGES); do \
		docker tag $$img:$(VERSION) $(OCP_PUSH_REGISTRY)/$$img:$(VERSION); \
		docker push $(OCP_PUSH_REGISTRY)/$$img:$(VERSION); \
	done
	@echo "All images pushed to $(OCP_PUSH_REGISTRY)"

.PHONY: deploy-openshift-crc
deploy-openshift-crc: kustomize cert-manager push-images-openshift-crc ## Deploy openshift-crc topology (requires CRC prerequisites + pushed images).
deploy-openshift-crc: REGISTRY = $(OCP_INTERNAL_REGISTRY)
deploy-openshift-crc:
	@echo "Deploying OpenShift CRC suite (namespace: $(OCP_NAMESPACE))..."
	@NS=$(OCP_NAMESPACE); \
	$(KUBECTL) apply -f suites/openshift-crc/namespace.yaml; \
	$(KUBECTL) apply -f suites/openshift-crc/scc.yaml; \
	$(KUBECTL) apply -f suites/openshift-crc/rbac.yaml -n $$NS; \
	$(KUBECTL) adm policy add-scc-to-user meridio-lb -z meridio-2-stateless-load-balancer-ocp -n $$NS; \
	$(KUBECTL) adm policy add-scc-to-user meridio-sidecar -z meridio-2-network-sidecar -n $$NS; \
	$(KUBECTL) adm policy add-scc-to-user privileged -z vpn-gateway -n $$NS
	@echo "Deploying VPN gateway pod (must be up before LB readiness gates can clear)..."
	@NS=$(OCP_NAMESPACE); \
	$(KUBECTL) apply -f suites/openshift-crc/nad.yaml -n $$NS; \
	sed 's|registry.nordix.org/cloud-native/meridio-2|$(OCP_INTERNAL_REGISTRY)|g; s|:latest|:$(VERSION)|g' \
		suites/openshift-crc/vpn-gateway.yaml | $(KUBECTL) apply -n $$NS -f -; \
	timeout=120; elapsed=0; \
	until $(KUBECTL) get pod vpn-gateway -n $$NS -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null | grep -q "True"; do \
		sleep 2; elapsed=$$((elapsed + 2)); \
		if [ $$elapsed -ge $$timeout ]; then echo "Timeout waiting for vpn-gateway pod"; exit 1; fi; \
	done; \
	echo "VPN gateway ready"
	$(call deploy-suite,$(OCP_NAMESPACE),$(shell pwd)/suites/openshift-crc,ocp,sllbr-gw-ocp,$(shell pwd)/suites/openshift-crc/lb-deployment.yaml)
	@echo "OpenShift CRC suite deployed successfully"

.PHONY: undeploy-openshift-crc
undeploy-openshift-crc: ## Undeploy openshift-crc topology.
	@NS=$(OCP_NAMESPACE); \
	$(KUBECTL) delete validatingwebhookconfiguration meridio-2-validating-webhook-configuration-ocp --ignore-not-found=true; \
	$(KUBECTL) delete -f suites/openshift-crc/scc.yaml --ignore-not-found=true; \
	$(KUBECTL) delete namespace $$NS --ignore-not-found=true
