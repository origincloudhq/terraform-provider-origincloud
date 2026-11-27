resource "origincloud_secret_manager_secret" "db" {
  project   = "my-project-123"
  region    = "eu-central1"
  secret_id = "billing-db-password"

  # Left out, the secret's payloads are encrypted under the project's default
  # Secret Manager key in the region. The key can't be changed afterwards.
  # kms_key = origincloud_kms_key.secrets.id

  tags = {
    team = "billing"
  }

  # Destroying a secret only schedules its deletion: it can be restored within
  # its recovery window, and its id stays taken until then. With force_delete
  # it is deleted at once, with all its versions, and the id is free again.
  # It is read from the state when the secret is destroyed, so apply it first.
  force_delete = false
}
