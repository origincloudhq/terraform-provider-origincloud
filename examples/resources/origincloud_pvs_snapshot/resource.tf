resource "origincloud_pvs_volume" "data" {
  project  = "my-project-123"
  zone     = "eu-central1-az1"
  size_gib = 20
}

resource "origincloud_pvs_snapshot" "data" {
  project       = "my-project-123"
  zone          = "eu-central1-az1"
  source_volume = origincloud_pvs_volume.data.name
  # Only a protected snapshot is the source of new volumes.
  protected = true
}

resource "origincloud_pvs_volume" "restored" {
  project         = "my-project-123"
  zone            = "eu-central1-az1"
  size_gib        = 20
  source_snapshot = origincloud_pvs_snapshot.data.name
}
