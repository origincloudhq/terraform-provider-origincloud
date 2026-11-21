resource "origincloud_compute_key_pair" "admin" {
  project = "my-project-123"
}

# A recipe for instances. Changing launch_template_data adds a version, which
# becomes the default unless default_version is set: the versions that exist
# are never changed.
resource "origincloud_compute_launch_template" "web" {
  project = "my-project-123"
  tags    = { "origin:name" = "web" }

  launch_template_data = {
    instance_type = "//compute.origincloudapis.eu/instanceTypes/gp1.core.2"
    image_family  = "//compute.origincloudapis.eu/imageFamilies/ubuntu-24-04-minimal"
    key_pair      = origincloud_compute_key_pair.admin.id
    tags          = { role = "web" }
  }
}
