# Origin Cloud provider for Terraform

Manages Origin Cloud resources with Terraform or OpenTofu. Its resources and
data sources are generated from the API definitions in
[origincloudhq/api](https://github.com/origincloudhq/api), over the
[Go SDK](https://github.com/origincloudhq/sdk-go).

| Type | Name | API |
|---|---|---|
| Resource | `origincloud_pvs_volume` | A block storage volume |
| Resource | `origincloud_pvs_snapshot` | A point-in-time copy of a volume |
| Data source | `origincloud_pvs_storage_class` | A storage tier a volume is created in |
| Data source | `origincloud_pvs_storage_classes` | Every storage class |
| Data source | `origincloud_pvs_volumes` | The volumes in a zone |
| Data source | `origincloud_pvs_snapshots` | The snapshots in a zone |
| Data source | `origincloud_location_region` | A region and its zones |
| Data source | `origincloud_location_regions` | Every region |
| Data source | `origincloud_location_zone` | A zone, its region and the services it offers |
| Data source | `origincloud_location_zones` | Every zone |

## Using it

```hcl
terraform {
  required_providers {
    origincloud = {
      source = "origincloudhq/origincloud"
    }
  }
}

provider "origincloud" {}

data "origincloud_pvs_storage_class" "general" {
  name = "storageClasses/gp1"
}

resource "origincloud_pvs_volume" "data" {
  project       = "my-project-123"
  zone          = "eu-central1-az1"
  size_gib      = 20
  storage_class = data.origincloud_pvs_storage_class.general.name
  tags          = { "origin:name" = "data" }
}

resource "origincloud_pvs_snapshot" "data" {
  project       = "my-project-123"
  zone          = "eu-central1-az1"
  source_volume = origincloud_pvs_volume.data.name
}
```

A list of zonal resources, such as `origincloud_pvs_volumes`, covers one
zone. For every zone's, list the zones that offer the service first:

```hcl
data "origincloud_location_zones" "all" {}

data "origincloud_pvs_volumes" "each" {
  for_each = toset([
    for z in data.origincloud_location_zones.all.zones :
    trimprefix(z.name, "zones/") if contains(z.services, "pvs")
  ])
  project = "my-project-123"
  zone    = each.key
}
```

`terraform init`, or `tofu init`, installs it from the
[Terraform Registry](https://registry.terraform.io/providers/origincloudhq/origincloud)
or the [OpenTofu registry](https://search.opentofu.org/provider/origincloudhq/origincloud),
which also show its documentation. Every release is signed with the
provider's own key, which both check.

## Configuration

Credentials come from the environment, the first of:

1. `ORIGINCLOUD_ACCESS_TOKEN`: an access token, sent as it is.
2. `ORIGINCLOUD_CLIENT_ID` and `ORIGINCLOUD_CLIENT_SECRET`: a service account's
   credential, exchanged for access tokens as they expire. This is what
   automation should use.
3. The session the `origin` CLI stored at its last login (`origin id login`).

| Argument | |
|---|---|
| `region` | The region whose front door the provider calls, such as `eu-central1`. Every front door serves every resource; a region only picks the nearest. A global service, such as location, is always called at its regionless host. Defaults to `ORIGINCLOUD_REGION`, and to the regionless endpoint when that is unset. |
| `endpoint` | The URL every call goes to instead, such as a test server's. |

## Behaviour

- **Operations.** Creating, updating and deleting wait until the change is
  complete, so a resource that depends on another finds it ready.
- **Replacement.** Changing an attribute the API can't change, such as a
  volume's `storage_class` or its `zone`, replaces the resource. Each
  attribute's description says when it does.
- **Failed creates.** A resource whose creation failed after it came to exist,
  such as a volume whose storage couldn't be created, is kept in the state as
  tainted, and the next apply replaces it.
- **Deleted resources.** A resource deleted outside Terraform leaves the state
  at the next refresh, and the next apply creates it again.
- **Protection.** A snapshot is the source of new volumes only while it is
  protected. `protected = true` protects it, and destroying it unprotects it
  first.
- **Lists and sets.** A repeated attribute is a list when the API keeps its
  order, and a set when it doesn't: reordering a list is a change, reordering
  a set isn't. An attribute left out stays null and one set to `[]` stays
  `[]`; neither plans a change when the API answers with nothing. Changing
  any part of a nested attribute updates it whole.
- **Import.** Every resource is imported by its resource name:

  ```sh
  terraform import origincloud_pvs_volume.data projects/my-project-123/zones/eu-central1-az1/volumes/vol-1a2b3c
  ```

## Versions

The resources follow their API's version: PVS and location are `v1beta1`, a
preview that may change in incompatible ways, like the API itself.

## Contributing

Issues and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Mozilla Public License 2.0; see [LICENSE](LICENSE).
