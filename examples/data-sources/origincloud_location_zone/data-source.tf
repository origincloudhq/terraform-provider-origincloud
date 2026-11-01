data "origincloud_location_zone" "az1" {
  name = "zones/eu-central1-az1"
}

output "offers_pvs" {
  value = contains(data.origincloud_location_zone.az1.services, "pvs")
}
