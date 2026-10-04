# Write-only: the webhook URL never reaches state (Terraform/OpenTofu 1.11+).
resource "updawg_notification_channel" "ops" {
  name              = "ops"
  kind              = "slack"
  config_wo         = jsonencode({ webhook_url = var.slack_webhook_url })
  config_wo_version = 1
}

# Or kept, sensitive, in state.
resource "updawg_notification_channel" "oncall" {
  name   = "on-call"
  kind   = "pagerduty"
  config = jsonencode({ routing_key = var.pagerduty_routing_key })
}

variable "slack_webhook_url" {
  type      = string
  sensitive = true
}

variable "pagerduty_routing_key" {
  type      = string
  sensitive = true
}
