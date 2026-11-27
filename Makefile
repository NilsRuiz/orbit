SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

COMPOSE := docker compose -f hack/compose/docker-compose.yml
NATS_BOX := docker run --rm --network orbit-net natsio/nats-box:latest
TF_ENV ?= dev
TF_DIR := infra/terraform/envs/$(TF_ENV)

##@ Help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2} /^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5)}' $(MAKEFILE_LIST)

##@ Quality gates (same checks as CI)
verify: verify-go verify-tf verify-gitops verify-shell ## Run every quality gate

verify-go: ## gofmt, go vet, golangci-lint, go test -race (agent, operator)
	@for m in agent operator; do \
	  [ -f $$m/go.mod ] || continue; \
	  echo "==> $$m"; \
	  ( cd $$m && test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt needed"; exit 1; } ) || exit 1; \
	  if [ -f $$m/Makefile ]; then \
	    $(MAKE) -s -C $$m test || exit 1; \
	  else \
	    ( cd $$m && go vet ./... && go test -race -count=1 ./... ) || exit 1; \
	  fi; \
	  if command -v golangci-lint >/dev/null; then (cd $$m && golangci-lint run) || exit 1; \
	  else echo "⚠️  golangci-lint not installed: lint NOT verified"; fi; \
	done

verify-tf: ## terraform fmt -check, validate (no backend), tflint
	terraform fmt -check -recursive infra/terraform
	@for d in infra/terraform/modules/* infra/terraform/envs/* infra/terraform/bootstrap; do \
	  echo "==> $$d"; \
	  terraform -chdir=$$d init -backend=false -input=false >/dev/null && terraform -chdir=$$d validate || exit 1; \
	done
	@if command -v tflint >/dev/null; then tflint --recursive --chdir infra/terraform; \
	else echo "⚠️  tflint not installed: lint NOT verified"; fi

verify-gitops: ## kustomize build | kubeconform for every kustomization
	@if ! command -v kustomize >/dev/null || ! command -v kubeconform >/dev/null; then \
	  echo "⚠️  kustomize/kubeconform not installed: gitops NOT verified"; exit 0; fi; \
	for k in $$(find gitops -name kustomization.yaml -exec dirname {} \;); do \
	  echo "==> $$k"; kustomize build $$k | kubeconform -strict -summary -ignore-missing-schemas || exit 1; \
	done

verify-shell: ## shellcheck all scripts
	shellcheck hack/scripts/*.sh hack/k3d/*.sh hack/payload/*.sh .claude/hooks/*.sh

##@ Agent
agent-test: ## Run agent unit tests
	cd agent && go test -race ./...

agent-lint: ## Lint agent (requires golangci-lint)
	cd agent && golangci-lint run

agent-build: ## Build agent binary
	cd agent && CGO_ENABLED=0 go build -o ../bin/orbit-agent ./cmd/orbit-agent

##@ Local fleet (docker compose)
dev-up: ## Start hub NATS + 3 edge leaf nodes + 3 agents
	@test -f agent/go.sum || (cd agent && go mod tidy)
	$(COMPOSE) up -d --build
	@./hack/scripts/init-hub.sh

dev-down: ## Stop local fleet
	$(COMPOSE) down -v

logs: ## Follow agent logs
	$(COMPOSE) logs -f agent-sat-1 agent-sat-2 agent-sat-3

desired: ## Set desired version: make desired NODE=sat-1 VERSION=1.1.0
	@test -n "$(NODE)" -a -n "$(VERSION)" || (echo "usage: make desired NODE=sat-1 VERSION=1.1.0"; exit 1)
	$(NATS_BOX) nats --server nats://hub:4222 kv put orbit-desired $(NODE) '{"version":"$(VERSION)","rolloutId":"manual"}'

NODES ?= sat-1 sat-2 sat-3
status: ## Last status each node reported to the hub (ORBIT_STATUS): make status [NODES="sat-1 sat-2"]
	@# `nats stream view` pages interactively and fails without a TTY: read the last message
	@# per subject instead, decoded with the jq shipped in nats-box.
	@$(NATS_BOX) sh -c 'set -o pipefail; for n in $(NODES); do \
	  nats --server nats://hub:4222 stream get ORBIT_STATUS --last-for orbit.edge.$$n.status --json 2>/dev/null \
	  | jq -r ".data | @base64d | fromjson | \"\(.nodeId)\t\(.phase)\t\(.current)\t\(.message // \"\")\"" \
	  || echo "$$n	no status yet"; done'

link-cut: ## Cut the link of an edge node: make link-cut NODE=sat-2
	./hack/scripts/link.sh cut $(NODE)

link-blackhole: ## Silently drop all traffic of an edge link (real radio loss): make link-blackhole NODE=sat-2
	./hack/scripts/link.sh blackhole $(NODE)

link-restore: ## Restore the link of an edge node
	./hack/scripts/link.sh restore $(NODE)

link-degrade: ## Add latency/loss on an edge link: make link-degrade NODE=sat-2
	./hack/scripts/link.sh degrade $(NODE)

##@ Hub cluster (k3d): the operator drives the compose fleet
# The demo-* targets act on this hub; the fleet-* ones below set it to the fleet's hub.
HUB_CONTEXT ?= k3d-orbit-hub
HUB_KUBECTL := kubectl --context $(HUB_CONTEXT)

hub-up: ## Create the k3d hub cluster and deploy the operator (re-run after a change)
	./hack/k3d/hub.sh up

hub-down: ## Delete the k3d hub cluster
	./hack/k3d/hub.sh down

demo-apply: ## Apply the demo Fleet and Rollout (hack/demo)
	$(HUB_KUBECTL) apply -f hack/demo/fleet.yaml -f hack/demo/rollout.yaml

demo-release: ## Release a version to the demo fleet: make demo-release VERSION=1.2.0
	@test -n "$(VERSION)" || (echo "usage: make demo-release VERSION=1.2.0"; exit 1)
	$(HUB_KUBECTL) patch rollout payload --type merge -p '{"spec":{"targetVersion":"$(VERSION)"}}'

demo-watch: ## Watch nodes, fleets and rollouts (refreshed every 2 s, Ctrl-C to stop)
	@# kubectl get -w only watches a single resource type: redraw all three instead.
	@while :; do \
	  out=$$($(HUB_KUBECTL) get edgenodes,fleets,rollouts -o wide 2>&1); \
	  printf '\033[H\033[2J%s  (every 2 s, Ctrl-C to stop)\n\n%s\n' "$$(date +%T)" "$$out"; \
	  sleep 2; \
	done

##@ Kubernetes fleet (k3d): hub and satellites all from GitOps
FLEET_HUB := HUB_CONTEXT=k3d-orbit-fleet-hub

fleet-up: ## Create the hub + 3 satellite clusters from GitOps (SOURCE=local: this working tree)
	./hack/k3d/fleet.sh up

fleet-down: ## Delete the k3d fleet
	./hack/k3d/fleet.sh down

fleet-demo-apply: ## Apply the demo Fleet and Rollout on the fleet's hub
	@$(MAKE) -s demo-apply $(FLEET_HUB)

fleet-release: ## Release a version to the k3d fleet: make fleet-release VERSION=1.2.0
	@$(MAKE) -s demo-release VERSION=$(VERSION) $(FLEET_HUB)

fleet-watch: ## Watch the k3d fleet's nodes, fleets and rollouts
	@$(MAKE) -s demo-watch $(FLEET_HUB)

fleet-link-cut: ## Silently cut a satellite's link to the hub: make fleet-link-cut NODE=sat-1
	@test -n "$(NODE)" || (echo "usage: make fleet-link-cut NODE=sat-1"; exit 1)
	./hack/k3d/fleet.sh link-cut $(NODE)

fleet-link-restore: ## Restore a satellite's link: make fleet-link-restore NODE=sat-1
	@test -n "$(NODE)" || (echo "usage: make fleet-link-restore NODE=sat-1"; exit 1)
	./hack/k3d/fleet.sh link-restore $(NODE)

##@ Infrastructure
tf-fmt: ## Format Terraform
	terraform fmt -recursive infra/terraform

tf-plan: ## Terraform plan (TF_ENV=dev)
	terraform -chdir=$(TF_DIR) init -input=false && terraform -chdir=$(TF_DIR) plan

tf-apply: ## Terraform apply (TF_ENV=dev)
	terraform -chdir=$(TF_DIR) apply

tf-destroy: ## Destroy everything — run after each demo session!
	terraform -chdir=$(TF_DIR) destroy

.PHONY: help verify verify-go verify-tf verify-gitops verify-shell agent-test agent-lint agent-build dev-up dev-down logs desired status \
        link-cut link-blackhole link-restore link-degrade hub-up hub-down demo-apply demo-release demo-watch \
        fleet-up fleet-down fleet-demo-apply fleet-release fleet-watch fleet-link-cut fleet-link-restore tf-fmt tf-plan tf-apply tf-destroy
