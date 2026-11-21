# A template at its default version, or at one: `@3`, `@default` or `@latest`.
data "origincloud_compute_launch_template" "web" {
  name = "projects/my-project-123/launchTemplates/lt-1a2b3c@latest"
}

output "newest_instance_type" {
  value = data.origincloud_compute_launch_template.web.launch_template_data.instance_type
}
