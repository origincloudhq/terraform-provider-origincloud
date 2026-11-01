data "origincloud_location_zones" "all" {}

# The zones that offer PVS, as a volume's `zone` names them.
locals {
  pvs_zones = [for z in data.origincloud_location_zones.all.zones : trimprefix(z.name, "zones/") if contains(z.services, "pvs")]
}

# Every zone's volumes.
data "origincloud_pvs_volumes" "each" {
  for_each = toset(local.pvs_zones)
  project  = "my-project-123"
  zone     = each.key
}
