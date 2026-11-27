# The k3s payload driver tests run against envtest (a real kube-apiserver and etcd), like
# the operator's. Versions are derived from go.mod, so they move with the dependencies.
LOCALBIN ?= $(CURDIR)/bin
ENVTEST ?= $(LOCALBIN)/setup-envtest
gomodver = $(shell go list -m -f '{{.Version}}' $(1) 2>/dev/null)
# setup-envtest is released with controller-runtime; the API server matches k8s.io/api.
ENVTEST_VERSION ?= $(call gomodver,sigs.k8s.io/controller-runtime)
ENVTEST_K8S_VERSION ?= $(shell echo '$(call gomodver,k8s.io/api)' | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

.PHONY: test
test: setup-envtest ## go vet, then go test -race against envtest
	go vet ./...
	KUBEBUILDER_ASSETS="$$("$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
	  go test -race -count=1 ./...

.PHONY: setup-envtest
setup-envtest: $(ENVTEST)
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path >/dev/null

$(ENVTEST):
	GOBIN="$(LOCALBIN)" go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)
