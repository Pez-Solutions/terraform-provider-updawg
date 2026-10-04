# Every web host that needs a reboot.
data "updawg_hosts" "web_reboots" {
  labels          = { tier = "web" }
  reboot_required = ["yes"]
}

# Hand-pick them into a group.
resource "updawg_group" "reboot_tonight" {
  name  = "reboot tonight"
  hosts = data.updawg_hosts.web_reboots.ids
}
