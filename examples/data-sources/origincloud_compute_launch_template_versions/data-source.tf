data "origincloud_compute_launch_template_versions" "web" {
  launch_template = "//compute.origincloudapis.eu/projects/my-project-123/launchTemplates/lt-1a2b3c"
}

# The versions' numbers, the newest first.
output "versions" {
  value = [for v in data.origincloud_compute_launch_template_versions.web.versions : v.version]
}
