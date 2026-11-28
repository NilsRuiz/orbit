# tflint for every module (tflint --recursive --chdir infra/terraform).
# The built-in terraform ruleset with its recommended preset, plus documented variables:
# every variable has a type and a description (CLAUDE.md, Terraform conventions).
config {
  call_module_type = "local"
}

plugin "terraform" {
  enabled = true
  preset  = "recommended"
}

rule "terraform_documented_variables" {
  enabled = true
}

rule "terraform_typed_variables" {
  enabled = true
}
