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

After step 2, copy the `github_wif_provider` and `github_ci_service_account` outputs into
the repository variables `GCP_WIF_PROVIDER` and `GCP_CI_SA` so CI can run `terraform plan`
without any stored key.
