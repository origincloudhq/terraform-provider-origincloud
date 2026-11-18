resource "origincloud_pvs_volume" "data" {
  project       = "my-project-123"
  zone          = "eu-central1-az1"
  size_gib      = 20
  storage_class = "//pvs.origincloudapis.eu/storageClasses/gp1"
  tags          = { "origin:name" = "data" }
}
