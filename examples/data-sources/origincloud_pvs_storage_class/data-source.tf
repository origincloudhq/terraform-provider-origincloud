data "origincloud_pvs_storage_class" "general" {
  name = "storageClasses/gp1"
}

output "baseline_iops" {
  value = data.origincloud_pvs_storage_class.general.baseline_iops
}
