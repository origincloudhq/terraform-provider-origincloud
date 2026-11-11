# A service account without a secret, for CI jobs to act as.
resource "origincloud_iam_service_account" "deploy" {
  organization = "my-org-123"
  display_name = "Deploy"
  type         = "WORKLOAD"
}

resource "origincloud_iam_role_binding" "deploy" {
  organization = "my-org-123"
  member       = origincloud_iam_service_account.deploy.member
  role         = "roles/editor"
  project      = "projects/my-project-123"
}

# GitHub Actions jobs of one repository, on its main branch. repository_id
# is the repository's number, which a rename doesn't change.
resource "origincloud_iam_federated_credential" "github" {
  organization    = "my-org-123"
  service_account = basename(origincloud_iam_service_account.deploy.name)
  display_name    = "GitHub Actions"
  issuer          = "https://token.actions.githubusercontent.com"
  conditions = {
    repository_id = "123456789"
    ref           = "refs/heads/main"
  }
}

# GitLab CI jobs of one project, on its main branch.
resource "origincloud_iam_federated_credential" "gitlab" {
  organization    = "my-org-123"
  service_account = basename(origincloud_iam_service_account.deploy.name)
  display_name    = "GitLab CI"
  issuer          = "https://gitlab.com"
  conditions = {
    project_id = "12345678"
    ref        = "main"
  }
}
