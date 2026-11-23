#!/usr/bin/env bash
# Labels as code: create or update the repository's GitHub labels. Idempotent.
# Usage: ./hack/scripts/sync-labels.sh            (current repo, via gh)
#        ./hack/scripts/sync-labels.sh owner/repo
set -euo pipefail

repo_args=()
if [[ $# -ge 1 ]]; then
  repo_args=(--repo "$1")
fi

# name|color|description
labels=(
  "type/bug|d73a4a|Something behaves unexpectedly"
  "type/feature|a2eeef|New capability or improvement"
  "type/chore|cfd3d7|Maintenance, refactor, docs, tooling"
  "area/agent|5319e7|Edge agent (agent/)"
  "area/operator|1d76db|Kubernetes operator (operator/)"
  "area/infra|0e8a16|Terraform, GCP (infra/terraform/)"
  "area/gitops|fbca04|Flux manifests (gitops/)"
  "area/hack|c5def5|Local fleet and scripts (hack/)"
  "area/ci|bfdadc|GitHub Actions, release tooling"
  "priority/high|b60205|Blocks the current phase"
  "priority/low|ededed|Nice to have, can be deferred"
)

for entry in "${labels[@]}"; do
  IFS='|' read -r name color description <<<"$entry"
  gh label create "$name" --color "$color" --description "$description" --force ${repo_args[@]+"${repo_args[@]}"}  # bash 3.2-safe empty array
  echo "✓ $name"
done
