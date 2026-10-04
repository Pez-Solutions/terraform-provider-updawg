resource "updawg_enrollment_token" "web" {
  name       = "web tier"
  labels     = { tier = "web" }
  max_uses   = 20
  expires_at = "2027-01-01T00:00:00Z"
}

# The value, for cloud-init or a configuration management secret.
output "web_enrollment_token" {
  value     = updawg_enrollment_token.web.token
  sensitive = true
}
