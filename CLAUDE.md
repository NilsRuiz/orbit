# CLAUDE.md — Orbit

Orbit is a portfolio SRE / platform engineering project: GitOps fleet management for
intermittently-connected edge nodes (simulated satellites). Read `README.md` and
`docs/adr/` before any non-trivial change: ADRs are the source of truth for design decisions.

## Language

- Talk to me in **French**. I am learning: explain the *why* of non-obvious choices.
- Write code, comments, commit messages, PRs, docs and ADRs in **English**.

## Repository map

| Path | What | Checks |
|---|---|---|
| `agent/` | Go edge agent (NATS, A/B state, dead man's switch) | `make verify-go` |
| `operator/` | Go Kubernetes operator (kubebuilder) | `make verify-go` |
| `infra/terraform/` | GCP infra, modules + `envs/dev` | `make verify-tf` |
| `gitops/` | Flux manifests (hub + edge) | `make verify-gitops` |
| `hack/` | Local fleet (docker compose), chaos scripts, k3d | `make verify-shell` |
| `docs/adr/` | Architecture Decision Records | — |

## Commands

```bash
make verify            # everything below; run before every commit
make verify-go         # gofmt, go vet, golangci-lint, go test -race
make verify-tf         # terraform fmt -check, validate, tflint
make verify-gitops     # kustomize build | kubeconform
make verify-shell      # shellcheck
make dev-up / dev-down # local fleet; demo scenario in README
```

## Development workflow (summary — details in `CONTRIBUTING.md`)

Trunk-based development: `main` is always releasable, work happens on short-lived branches,
merged through a PR with green CI, **squash merge**.

1. **Never commit on `main`.** Start with `git switch -c <type>/<short-desc>` from an
   up-to-date `main` (e.g. `feat/agent-status-buffering`, `fix/kv-watch-reconnect`).
2. **Plan first** for anything over ~30 lines: state the approach, files touched, how it
   will be tested, and wait for my OK. If it changes a design decision, propose an ADR.
3. **Tests come with the change.** A bug fix starts with a test that fails without the fix.
4. Run `/verify` before each commit. Never weaken, skip or delete a test to make CI pass.
5. Small, atomic commits in **Conventional Commits** format (`/commit`).
6. Open the PR with `/pr`; watch CI; fix failures in new commits (no force-push while in review).

Skills in `.claude/skills/`: `/issue` (create an issue), `/feature` (full cycle), `/verify`,
`/commit`, `/self-review`, `/pr`, `/adr`.

## Definition of Done

A change is done only when:
- [ ] Behaviour is covered by tests (unit; integration for NATS/K8s interactions).
- [ ] `make verify` passes locally and CI is green.
- [ ] Docs updated where behaviour changed (README, component README, runbook, ADR).
- [ ] Observability considered: logs are structured (`slog`), new failure modes are visible.
- [ ] No secrets, no `TODO` without an issue reference, no dead code.

## Code conventions

**Go**
- Go ≥ 1.26, standard layout (`cmd/`, `internal/`). Keep `internal/reconcile` free of
  transport dependencies: business logic behind small interfaces, tested with fakes.
- Errors: wrap with context (`fmt.Errorf("load state: %w", err)`); never `panic` in runtime paths.
- Every goroutine has an owner and exits on `ctx.Done()`. Shared state is guarded
  (mutex/atomic); tests run with `-race`.
- Logging: `log/slog` JSON, key/value pairs, no `fmt.Println`.
- Table-driven tests; integration tests use the in-process NATS server
  (see `agent/internal/transport/nats_integration_test.go`).

**Terraform**
- Modules are reusable and provider-scoped; environments only compose modules.
- Pin provider versions; every variable has a type and a description; no hardcoded project IDs.
- Least privilege IAM; cost-aware defaults (ADR-0005).

**Kubernetes / GitOps**
- Resources have requests/limits, probes, `runAsNonRoot`, read-only root FS where possible.
- Pin chart versions once tested. Flux owns cluster state: never `kubectl apply` to the
  cloud clusters by hand.

## Hard rules (also enforced by hooks in `.claude/hooks/`)

- Never: force-push, push to `main`, commit with `--no-verify`, rewrite published history.
- Never run `terraform apply`/`destroy` yourself: prepare the plan, I apply.
- Never read, print or commit secrets (`.env`, `*.pem`, `*.key`, credentials, tfstate).
- Never add a dependency without saying why and checking it is maintained.
- Do not mark work as done if a check was skipped: say what was not verified.
