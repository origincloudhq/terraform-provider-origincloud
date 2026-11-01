data "origincloud_pvs_snapshots" "all" {
  project = "my-project-123"
  zone    = "eu-central1-az1"
}

# The snapshots volumes can be created from.
output "protected" {
  value = [for s in data.origincloud_pvs_snapshots.all.snapshots : s.name if s.protection_state == "PROTECTED"]
}
