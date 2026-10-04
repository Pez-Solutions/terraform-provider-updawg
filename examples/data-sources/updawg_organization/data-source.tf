data "updawg_organization" "this" {}

output "plan" {
  value = data.updawg_organization.this.plan
}
