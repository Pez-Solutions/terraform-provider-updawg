# Every host labelled tier=web, plus one picked by hand.
resource "updawg_group" "web" {
  name           = "web"
  description    = "Public-facing web servers"
  label_selector = { tier = "web" }
  hosts          = ["hst_0193a4b2c1d0"]
}
