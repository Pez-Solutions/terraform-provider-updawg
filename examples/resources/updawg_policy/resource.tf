# The document is what the portal's policy editor shows. `plan` validates it
# and says what saving it would do to the fleet.
resource "updawg_policy" "web_production" {
  yaml = file("${path.module}/policies/web-production.yaml")
}
