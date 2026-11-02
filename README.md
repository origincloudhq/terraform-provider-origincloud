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
| Resource | `origincloud_compute_instance` | A virtual machine in one zone |
| Resource | `origincloud_compute_key_pair` | An SSH public key, and the private key generated for it |
| Resource | `origincloud_compute_volume_attachment` | A volume an instance holds |
| Resource | `origincloud_compute_image` | A project's own image, which boot volumes are created from |
| Data source | `origincloud_compute_instance_type` | A size an instance is launched as |
| Data source | `origincloud_compute_instance_types` | Every instance type |
| Data source | `origincloud_compute_image_family` | The current public image of an operating system release |
| Data source | `origincloud_compute_image_families` | Every image family |
| Data source | `origincloud_compute_public_image` | One of the platform's images, in one zone |
| Data source | `origincloud_compute_public_images` | The public images in a zone |
| Data source | `origincloud_compute_images` | A project's own images in a zone |
| Data source | `origincloud_compute_instances` | The instances in a zone |
| Data source | `origincloud_compute_key_pairs` | A project's key pairs |
| Data source | `origincloud_compute_volume_attachments` | The volumes an instance holds |
| Resource | `origincloud_resourcemanager_organization` | The account projects, members and billing belong to |
| Resource | `origincloud_resourcemanager_project` | The namespace an organization's resources are created in |
| Resource | `origincloud_resourcemanager_project_quota` | A project's budgets, and how much of each it uses |
| Data source | `origincloud_resourcemanager_organization_quota` | The most of each resource an organization's projects may hold |
| Data source | `origincloud_resourcemanager_organizations` | The organizations you hold a role in |
| Data source | `origincloud_resourcemanager_projects` | An organization's projects |
| Resource | `origincloud_network_vpc` | A private network in one region |
| Resource | `origincloud_network_subnet` | The part of a VPC in one zone |
| Resource | `origincloud_network_internet_gateway` | A VPC's way to and from the internet |
| Resource | `origincloud_network_nat_gateway` | Shared IPv4 egress through one floating IP |
| Resource | `origincloud_network_route_table` | Routes out of a VPC for the subnets associated with it |
| Resource | `origincloud_network_route` | A default route through an internet or NAT gateway |
| Resource | `origincloud_network_route_table_association` | The route table a subnet uses |
| Resource | `origincloud_network_security_group` | Rules allowing traffic to and from network interfaces |
| Resource | `origincloud_network_security_group_rule` | The traffic a security group allows in one direction, to or from one peer |
| Resource | `origincloud_network_security_group_association` | A security group filtering a network interface's traffic |
| Resource | `origincloud_network_acl` | Stateless rules for the traffic in and out of subnets |
| Resource | `origincloud_network_acl_rule` | An ACL rule accepting or dropping traffic in one direction |
| Resource | `origincloud_network_acl_association` | The ACL filtering a subnet's traffic at its edge |
| Resource | `origincloud_network_network_interface` | Private addresses in one subnet, filtered by security groups |
| Resource | `origincloud_network_floating_ip` | A public IPv4 address of your own |
| Resource | `origincloud_network_floating_ip_association` | The network interface a floating IP reaches |
| Resource | `origincloud_network_dns_policy` | How a VPC's resolver answers names outside the platform's own |
| Resource | `origincloud_network_dns_policy_association` | The DNS policy a VPC's resolver relays through |
| Data source | `origincloud_network_vpcs` | The VPCs in a region |
| Data source | `origincloud_network_subnets` | The subnets in a zone |
| Data source | `origincloud_network_internet_gateways` | The internet gateways in a region |
| Data source | `origincloud_network_nat_gateways` | The NAT gateways in a zone |
| Data source | `origincloud_network_route_tables` | The route tables in a region |
| Data source | `origincloud_network_routes` | A route table's routes |
| Data source | `origincloud_network_route_table_associations` | A subnet's route table association |
| Data source | `origincloud_network_security_groups` | The security groups in a region |
| Data source | `origincloud_network_security_group_rules` | A security group's rules |
| Data source | `origincloud_network_security_group_associations` | A network interface's security group associations |
| Data source | `origincloud_network_acls` | The ACLs in a region |
| Data source | `origincloud_network_acl_rules` | An ACL's rules |
| Data source | `origincloud_network_acl_associations` | A subnet's ACL association |
| Data source | `origincloud_network_network_interfaces` | The network interfaces in a zone |
| Data source | `origincloud_network_floating_ips` | The floating IPs in a zone |
| Data source | `origincloud_network_floating_ip_associations` | A floating IP's association |
| Data source | `origincloud_network_dns_policies` | The DNS policies in a region |
| Data source | `origincloud_network_dns_policy_associations` | A VPC's DNS policy association |

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
- **Deleted resources.** A resource deleted outside Terraform, or left in a
  terminal state such as an instance's `TERMINATED`, leaves the state at the
  next refresh, and the next apply creates it again.
- **Times.** Timestamps are RFC 3339 strings, such as `2026-11-01T12:00:00Z`,
  and durations strings in seconds, such as `90s`. A timestamp written in
  another time zone stays as written while it is the same instant.
- **Secrets.** A value the API returns only once, when the resource is
  created, such as a generated private key, stays in the state from then on.
  It is sensitive, which hides it in plans and output, but the state holds
  it as plain text: protect the state. An imported resource has none.
- **Values the API chooses.** An optional attribute the API fills in when it
  is left out, such as a default, keeps the API's value; removing it from the
  configuration keeps the current value rather than resetting it.
- **Singletons.** A resource its parent has exactly one of, such as a
  project's quota, exists with its parent: creating it in Terraform updates
  it with the values configured. Removing a value from the configuration
  unsets it, or returns it to its default, and destroying the resource
  unsets the values Terraform set, leaving the others. A value is
  Terraform's once Terraform has sent it: one configured with the value
  the API already has, as after an import, is left as it is on destroy. To stop managing one without unsetting anything,
  remove it from the state: a `removed` block with
  `lifecycle { destroy = false }`, or `terraform state rm`. Manage each with
  one resource only.
- **Ids you choose.** A resource whose id you choose, such as a project's
  `project_id`, can't be replaced with the same id: a deleted resource's id
  isn't used again. Change the id along with what replaces it.
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

The resources follow their API's version: every service is `v1beta1`, a
preview that may change in incompatible ways, like the API itself.

## Contributing

Issues and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Mozilla Public License 2.0; see [LICENSE](LICENSE).
