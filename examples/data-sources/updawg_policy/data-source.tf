data "updawg_policy" "baseline" {
  name = "baseline"
}

output "baseline_version" {
  value = data.updawg_policy.baseline.version
}
