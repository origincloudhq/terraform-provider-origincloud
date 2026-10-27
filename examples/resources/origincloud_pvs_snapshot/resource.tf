resource "origincloud_pvs_volume" "data" {
  project  = "my-project-123"
  zone     = "eu-central1-az1"
  size_gib = 20
}

resource "origincloud_pvs_snapshot" "data" {
  project       = "my-project-123"
  zone          = "eu-central1-az1"
  source_volume = origincloud_pvs_volume.data.name
}
