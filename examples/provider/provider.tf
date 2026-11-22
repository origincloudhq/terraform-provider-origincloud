terraform {
  required_providers {
    origincloud = {
      source = "origincloudhq/origincloud"
    }
  }
}

# Credentials come from credentials_file, a service account key's
# credentials file, or else from the environment, such as
# ORIGINCLOUD_CREDENTIALS_FILE or a CI job's OIDC token.
provider "origincloud" {
  region           = "eu-central1"
  credentials_file = "/etc/origincloud/deployer.json"
}
