terraform {
  required_providers {
    origincloud = {
      source = "origincloudhq/origincloud"
    }
  }
}

# Credentials come from the environment, such as ORIGINCLOUD_CLIENT_ID and
# ORIGINCLOUD_CLIENT_SECRET for a service account.
provider "origincloud" {
  region = "eu-central1"
}
