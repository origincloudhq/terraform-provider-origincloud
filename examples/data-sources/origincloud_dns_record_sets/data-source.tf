data "origincloud_dns_record_sets" "example" {
  project     = "my-project-123"
  hosted_zone = "hz-XEkE66gA3ma"
}

# The zone's names and types.
output "records" {
  value = [for r in data.origincloud_dns_record_sets.example.record_sets : "${r.dns_name} ${r.type}"]
}
