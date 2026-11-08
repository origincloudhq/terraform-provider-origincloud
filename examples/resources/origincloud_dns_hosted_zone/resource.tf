resource "origincloud_dns_hosted_zone" "example" {
  project    = "my-project-123"
  dns_name   = "example.com"
  visibility = "PUBLIC"
  tags       = { "origin:name" = "example" }
}

# The TXT record that proves control of the domain, to create where the
# domain is hosted now. The zone is PENDING_VERIFICATION until the
# platform's check finds it, and is deleted if it doesn't within 7 days.
output "challenge" {
  value = {
    name  = origincloud_dns_hosted_zone.example.challenge.dns_name
    type  = "TXT"
    value = origincloud_dns_hosted_zone.example.challenge.token
  }
}

# The name servers to delegate the domain to, at its registrar.
output "name_servers" {
  value = origincloud_dns_hosted_zone.example.name_servers
}
