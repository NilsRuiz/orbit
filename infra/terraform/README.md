# Infrastructure (Terraform, GCP)

```
bootstrap/        one-time: state bucket + required APIs (local state)
modules/
  network/        VPC, subnet with pod/service ranges, firewall, optional Cloud NAT
  gke-hub/        zonal GKE Standard cluster + spot node pool + least-privilege SA
  edge-node/      N spot VMs running k3s (simulated satellites)
  github-oidc/    Workload Identity Federation for GitHub Actions (no JSON keys)
envs/
  dev/            composition of the modules, remote state in GCS
```

## First run

```bash
gcloud auth application-default login

# 1. Bootstrap (once)
cd infra/terraform/bootstrap
terraform init && terraform apply -var project_id=<PROJECT_ID>

# 2. Environment
cd ../envs/dev
cp terraform.tfvars.example terraform.tfvars   # edit values
terraform init -backend-config="bucket=<PROJECT_ID>-orbit-tfstate"
terraform apply

# 3. Always, after a demo session
terraform destroy
```

After step 2, set four repository variables (Settings → Secrets and variables → Actions →
Variables), so CI runs `terraform plan` on pull requests with no stored key (Workload
Identity Federation):

| Variable | Value |
|---|---|
| `GCP_WIF_PROVIDER` | output `github_wif_provider` |
| `GCP_CI_SA` | output `github_ci_service_account` |
| `GCP_STATE_BUCKET` | output `state_bucket` of `bootstrap` |
| `GCP_PROJECT_ID` | the project ID |

The CI service account can only read (`roles/viewer`, `roles/iam.securityReviewer`) and use
the state bucket: CI plans, it never applies.

Provider versions are locked in each root module's `.terraform.lock.hcl` (Linux and macOS,
amd64 and arm64). Upgrade them with `terraform init -upgrade`, then `terraform providers
lock` for the four platforms, in a PR.
