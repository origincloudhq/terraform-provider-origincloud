data "origincloud_location_region" "central" {
  name = "regions/eu-central1"
}

output "zones" {
  value = data.origincloud_location_region.central.zones
}
