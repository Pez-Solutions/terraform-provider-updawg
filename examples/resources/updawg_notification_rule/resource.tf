resource "updawg_notification_rule" "security" {
  name        = "high-severity security to ops"
  event_types = ["proposal.opened", "proposal.auto_merged"]
  channel_ids = [updawg_notification_channel.ops.id]
  filter = {
    min_severity   = "high"
    proposal_kinds = ["security"]
  }
}

# A rule with no channels silences auto-merge notifications.
resource "updawg_notification_rule" "quiet" {
  name        = "no auto-merge noise"
  event_types = ["proposal.auto_merged"]
  channel_ids = []
}
