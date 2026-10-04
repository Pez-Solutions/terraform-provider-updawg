terraform {
  required_providers {
    updawg = {
      source = "pez-solutions/updawg"
    }
  }
}

# The token comes from UPDAWG_API_TOKEN; keep it out of .tf files.
provider "updawg" {
  org = "acme"
}
