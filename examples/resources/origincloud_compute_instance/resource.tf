resource "origincloud_compute_key_pair" "admin" {
  project = "my-project-123"
}

# Launched from its own arguments.
resource "origincloud_compute_instance" "web" {
  project            = "my-project-123"
  zone               = "eu-central1-az1"
  instance_type      = "//compute.origincloudapis.eu/instanceTypes/gp1.core.2"
  image_family       = "//compute.origincloudapis.eu/imageFamilies/ubuntu-24-04-minimal"
  key_pair           = origincloud_compute_key_pair.admin.id
  network_interfaces = ["//network.origincloudapis.eu/projects/my-project-123/zones/eu-central1-az1/networkInterfaces/eni-1a2b3c"]
  tags               = { "origin:name" = "web" }
}

# Launched from version 1 of a launch template, with its own tags over the
# version's. `@default` and `@latest` name the template's default and newest
# version; a bare name is its default.
resource "origincloud_compute_launch_template" "web" {
  project = "my-project-123"
  launch_template_data = {
    instance_type = "//compute.origincloudapis.eu/instanceTypes/gp1.core.2"
    image_family  = "//compute.origincloudapis.eu/imageFamilies/ubuntu-24-04-minimal"
    key_pair      = origincloud_compute_key_pair.admin.id
  }
}

resource "origincloud_compute_instance" "from_template" {
  project            = "my-project-123"
  zone               = "eu-central1-az1"
  launch_template    = "${origincloud_compute_launch_template.web.id}@1"
  network_interfaces = ["//network.origincloudapis.eu/projects/my-project-123/zones/eu-central1-az1/networkInterfaces/eni-4d5e6f"]
  tags               = { "origin:name" = "from-template" }
}
