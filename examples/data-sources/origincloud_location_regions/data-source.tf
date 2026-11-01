data "origincloud_location_regions" "all" {}

output "regions" {
  value = data.origincloud_location_regions.all.regions[*].name
}
