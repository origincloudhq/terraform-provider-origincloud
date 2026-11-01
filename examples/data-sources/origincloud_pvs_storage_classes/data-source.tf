data "origincloud_pvs_storage_classes" "all" {}

# The classes on sale.
output "available" {
  value = [for c in data.origincloud_pvs_storage_classes.all.storage_classes : c.name if c.available]
}
