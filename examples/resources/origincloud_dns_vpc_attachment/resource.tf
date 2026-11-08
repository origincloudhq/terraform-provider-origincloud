resource "origincloud_network_vpc" "main" {
  project = "my-project-123"
  region  = "eu-central1"
}

resource "origincloud_dns_hosted_zone" "corp" {
  project    = "my-project-123"
  dns_name   = "corp.example"
  visibility = "PRIVATE"
}

# The zone's names resolve from every instance in the VPC, which is in the
# zone's project.
resource "origincloud_dns_vpc_attachment" "main" {
  project     = "my-project-123"
  hosted_zone = basename(origincloud_dns_hosted_zone.corp.name)
  vpc         = origincloud_network_vpc.main.name
}
