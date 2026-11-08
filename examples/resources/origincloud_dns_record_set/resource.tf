resource "origincloud_dns_hosted_zone" "example" {
  project    = "my-project-123"
  dns_name   = "example.com"
  visibility = "PUBLIC"
}

# Values of its own.
resource "origincloud_dns_record_set" "www" {
  project     = "my-project-123"
  hosted_zone = basename(origincloud_dns_hosted_zone.example.name)
  dns_name    = "www.example.com"
  type        = "A"
  ttl         = "60s"
  basic       = { values = ["192.0.2.10", "192.0.2.11"] }
}

resource "origincloud_dns_record_set" "mail" {
  project     = "my-project-123"
  hosted_zone = basename(origincloud_dns_hosted_zone.example.name)
  dns_name    = "example.com"
  type        = "MX"
  ttl         = "3600s"
  basic       = { values = ["10 mail.example.com"] }
}

# An alias: the record set answers with the floating IP's address, and
# follows it as it changes. An alias is of type A.
resource "origincloud_network_floating_ip" "web" {
  project = "my-project-123"
  zone    = "eu-central1-az1"
}

resource "origincloud_dns_record_set" "apex" {
  project     = "my-project-123"
  hosted_zone = basename(origincloud_dns_hosted_zone.example.name)
  dns_name    = "example.com"
  type        = "A"
  ttl         = "300s"
  alias       = { floating_ip = origincloud_network_floating_ip.web.name }
}
