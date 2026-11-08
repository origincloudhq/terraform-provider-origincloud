data "origincloud_dns_hosted_zones" "all" {
  project = "my-project-123"
}

# A zone by its domain.
output "example" {
  value = one([for z in data.origincloud_dns_hosted_zones.all.hosted_zones : z.name if z.dns_name == "example.com"])
}

# The public zones that haven't proved control of their domain yet.
output "pending" {
  value = [for z in data.origincloud_dns_hosted_zones.all.hosted_zones : z.dns_name if z.state == "PENDING_VERIFICATION"]
}
