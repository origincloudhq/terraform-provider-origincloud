data "origincloud_pvs_volumes" "all" {
  project = "my-project-123"
  zone    = "eu-central1-az1"
}

# The volumes no instance holds.
output "detached" {
  value = [for v in data.origincloud_pvs_volumes.all.volumes : v.name if v.state == "AVAILABLE"]
}
