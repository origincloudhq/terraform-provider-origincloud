data "origincloud_secret_manager_secrets" "all" {
  project = "my-project-123"
  region  = "eu-central1"
}

# The secrets of one team.
output "billing_secrets" {
  value = [for s in data.origincloud_secret_manager_secrets.all.secrets : s.name if try(s.tags.team, "") == "billing"]
}
