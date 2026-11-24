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
| Resource | `origincloud_compute_launch_template` | A recipe for instances, with numbered versions that never change |
| Resource | `origincloud_compute_volume_attachment` | A volume an instance holds |
| Resource | `origincloud_compute_image` | A project's own image, which boot volumes are created from |
| Data source | `origincloud_compute_instance_type` | A size an instance is launched as |
| Data source | `origincloud_compute_instance_types` | Every instance type |
| Data source | `origincloud_compute_image_family` | The current public image of an operating system release |
| Data source | `origincloud_compute_image_families` | Every image family |
| Data source | `origincloud_compute_public_image` | One of the platform's images, in one zone |
| Data source | `origincloud_compute_public_images` | The public images in a zone |
| Data source | `origincloud_compute_images` | A project's own images in a zone |
| Data source | `origincloud_compute_launch_template` | A launch template at its default, newest or a numbered version |
| Data source | `origincloud_compute_launch_template_versions` | A launch template's versions, the newest first |
| Data source | `origincloud_compute_launch_templates` | A project's launch templates, at their default versions |
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
| Resource | `origincloud_network_resolver_policy` | How a VPC's resolver answers names outside the platform's own |
| Resource | `origincloud_network_resolver_policy_association` | The resolver policy a VPC's resolver relays through |
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
| Data source | `origincloud_network_resolver_policies` | The resolver policies in a region |
| Data source | `origincloud_network_resolver_policy_associations` | A VPC's resolver policy association |
| Resource | `origincloud_iam_service_account` | An identity of an organization for software |
| Resource | `origincloud_iam_role_binding` | One role granted to one member, organization-wide or in one project |
| Resource | `origincloud_iam_federated_credential` | A `WORKLOAD` service account's trust in a CI provider's OIDC tokens |
| Data source | `origincloud_iam_role` | A predefined set of permissions |
| Data source | `origincloud_iam_roles` | Every predefined role |
| Data source | `origincloud_iam_permission` | One action the platform checks |
| Data source | `origincloud_iam_permissions` | Every permission |
| Data source | `origincloud_iam_role_bindings` | An organization's role bindings, or your own in every organization |
| Data source | `origincloud_iam_service_accounts` | An organization's service accounts |
| Data source | `origincloud_iam_service_account_key` | A key an `EXTERNAL` service account authenticates with, by its public metadata |
| Data source | `origincloud_iam_service_account_keys` | A service account's keys, by their public metadata |
| Data source | `origincloud_iam_federated_credentials` | A service account's federated credentials |
| Data source | `origincloud_iam_user` | You, as the platform knows you |
| Data source | `origincloud_iam_audit_log_entry` | One recorded change to who may do what |
| Data source | `origincloud_iam_audit_log_entries` | An organization's audit log, most recent first |
| Resource | `origincloud_billing_billing_account` | Who an organization's invoices are issued to, and how its projects' usage is paid |
| Resource | `origincloud_billing_project_billing_info` | The billing account that pays for a project |
| Resource | `origincloud_billing_spend_limit` | The most a billing account means to spend in a month, and who is alerted |
| Data source | `origincloud_billing_billing_accounts` | An organization's billing accounts |
| Data source | `origincloud_billing_billing_country` | A country a billing address may be in, and whether it needs a VAT id |
| Data source | `origincloud_billing_billing_countries` | Every billing country |
| Data source | `origincloud_billing_legal_document` | A legal document: every version of one kind of document |
| Data source | `origincloud_billing_legal_documents` | The published legal documents |
| Data source | `origincloud_billing_legal_document_version` | One published version of a legal document |
| Data source | `origincloud_billing_legal_document_versions` | A legal document's published versions, the latest first |
| Data source | `origincloud_billing_legal_acceptance` | A billing account's acceptance of one version of a legal document |
| Data source | `origincloud_billing_legal_acceptances` | A billing account's acceptances, the latest first |
| Data source | `origincloud_support_case` | One question or problem, and the conversation with support about it |
| Data source | `origincloud_support_cases` | An organization's support cases, newest first |
| Data source | `origincloud_support_case_message` | One message in a support case |
| Data source | `origincloud_support_case_messages` | A support case's messages, oldest first |
| Resource | `origincloud_dns_hosted_zone` | A domain a project's records are answered for, public or private |
| Resource | `origincloud_dns_record_set` | Every record of one name and type in a hosted zone |
| Resource | `origincloud_dns_vpc_attachment` | A VPC a private zone answers in |
| Data source | `origincloud_dns_hosted_zones` | A project's hosted zones |
| Data source | `origincloud_dns_record_sets` | A hosted zone's record sets |
| Data source | `origincloud_dns_vpc_attachments` | A private zone's VPC attachments |
| Resource | `origincloud_kms_key` | A key whose material the platform keeps and never lets leave it |
| Resource | `origincloud_kms_grant` | One host's use of one key for one operation, until it expires |
| Data source | `origincloud_kms_keys` | A project's keys in a region |
| Data source | `origincloud_kms_grants` | A key's grants that haven't expired |
| Data source | `origincloud_kms_public_key` | A signing key version's public key |

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
  storage_class = data.origincloud_pvs_storage_class.general.id
  tags          = { "origin:name" = "data" }
}

resource "origincloud_pvs_snapshot" "data" {
  project       = "my-project-123"
  zone          = "eu-central1-az1"
  source_volume = origincloud_pvs_volume.data.id
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

Credentials come from `credentials_file`, when it is set, and otherwise
from the environment, the first of:

1. `ORIGINCLOUD_ACCESS_TOKEN`: an access token, sent as it is.
2. `ORIGINCLOUD_CREDENTIALS_FILE`: the path of a service account key's
   credentials file, which `origin iam service-accounts keys create` or the
   console saved. The provider signs an assertion with the key and exchanges
   it at the security token service for the account's access tokens, again
   before each expires. This is what automation outside CI should use.
3. A CI job's OIDC token, exchanged at the security token service
   (`https://sts.origincloudapis.eu`) for a `WORKLOAD` service account's
   access tokens, so that no secret is stored in the CI system: the token in
   the file `ORIGINCLOUD_OIDC_TOKEN_FILE` names, or else the token
   `ORIGINCLOUD_OIDC_TOKEN` holds, as GitLab CI's `id_tokens` set it, or
   else an HCP Terraform run's workload identity token,
   `TFC_WORKLOAD_IDENTITY_TOKEN` (`TFC_WORKLOAD_IDENTITY_TOKEN_<TAG>` when
   `ORIGINCLOUD_TFC_WORKLOAD_IDENTITY_TAG` names the tag of a tagged
   audience), or else, in a GitHub Actions job with `permissions: id-token: write`, the
   job's. The service account needs a federated credential that trusts the
   CI provider, such as an `origincloud_iam_federated_credential`.
   `ORIGINCLOUD_OIDC_DISABLED=true` leaves this step out. The provider's
   [documentation](docs/index.md#authentication-from-ci) has GitHub Actions
   and GitLab CI examples, and
   [docs.origincloud.eu](https://docs.origincloud.eu/docs/iam/workload-identity-federation/)
   how to set up the federated credential.
4. The session the `origin` CLI stored at its last login (`origin id login`).
5. On an instance that runs as a service account, the instance's metadata
   service.

| Argument | |
|---|---|
| `region` | The region whose front door the provider calls, such as `eu-central1`. Every front door serves every resource; a region only picks the nearest. A global service, such as location, is always called at its regionless host. Defaults to `ORIGINCLOUD_REGION`, and to the regionless endpoint when that is unset. |
| `endpoint` | The URL every call goes to instead, such as a test server's. |
| `credentials_file` | The path of a service account key's credentials file, the provider's credentials before any of the environment's. |
| `impersonate_service_account` | The full name of a service account to act as, with the token of the credentials above, a person's: see below. Defaults to `ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT`; `""` impersonates no one. |

A person who runs Terraform on their own machine can act as a service
account instead of with their own rights: with only an `origin id login`
session and `impersonate_service_account`, or
`ORIGINCLOUD_IMPERSONATE_SERVICE_ACCOUNT`, set to the account's full name,
the provider exchanges the session's token at the security token service
for the account's, so that `terraform plan` and `apply` have only the
account's roles. The person needs the role
`roles/iam.service-account-token-creator` on the account. The audit log
names the account as the actor and the person as the acting party. A
refused exchange fails the plan; the provider never falls back to the
person's own token, and no token lands in the state. The
[provider's documentation](docs/index.md#impersonating-a-service-account)
says more.

The provider has no resource that creates a service account key, and no
data source returns one: the API answers with the private key once, which a
resource would keep in the state. Create a key with the CLI or the console
and hand its file over; the
[provider's documentation](docs/index.md#why-there-is-no-key-resource) says
more, and how to upgrade a state that holds a key from v0.21.

## Behaviour

- **Operations.** Creating, updating and deleting wait until the change is
  complete, so a resource that depends on another finds it ready.
- **Replacement.** Changing an attribute the API can't change, such as a
  volume's `storage_class` or its `zone`, replaces the resource. Each
  attribute's description says when it does.
- **Failed creates.** A resource whose creation failed after it came to exist,
  such as a volume whose storage couldn't be created, is kept in the state as
  tainted, and the next apply replaces it. So is a resource with a secret,
  such as a generated key pair, whose create answer was lost: the API
  refuses the repeated create, as the secret went out with the lost answer
  only.
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
- **Names.** A resource is identified by its full resource name,
  `//{service}.origincloudapis.eu/{relative name}`, such as
  `//pvs.origincloudapis.eu/projects/my-project-123/zones/eu-central1-az1/volumes/vol-1`.
  It is the resource's `id`, and what an argument that refers to another
  resource takes, whichever service or the resource's project it is in:
  `source_volume = origincloud_pvs_volume.data.id`. `name` is the relative
  name, `projects/{project}/zones/{zone}/volumes/{volume}`, which `project`,
  `zone` and the other parts of a resource's own place are taken from. An
  argument that refers to a resource refuses a relative name at the plan,
  naming the `id` to use instead. A role binding's `member` and `role`, and a
  resource's `creator`, are full names on `iam` too. An import takes either
  form, such as `terraform import origincloud_pvs_volume.data
  //pvs.origincloudapis.eu/projects/my-project-123/zones/eu-central1-az1/volumes/vol-1`.
  The platform refuses a reference to a resource of another project in the
  places a resource has to be in its own, such as the volume of an attachment,
  and says so.
- **A child's parent.** The argument of a resource that names the resource
  it is a child of, such as a security group rule's `security_group`, a
  route's `route_table` or a volume attachment's `instance`, takes the
  parent's `id`, like any other reference; the list data sources of such
  resources take it too. The plan refuses the bare id, the relative `name`,
  a resource of another type, and one in another `project`, `region` or
  `zone` than the child says it is in. A listing takes `-` for the parent's
  last segment, `.../instances/-`, to list across parents.
- **Upgrading.** After v0.18, a resource's state has its `id` as the full
  name, and no `urn`. Terraform upgrades the state it holds the first time it
  reads it, without a change to apply; the upgrade stays in the provider for
  every state written before. Change the arguments that refer to another
  resource from its `name` to its `id`, and write `role`, `member` and the
  like as the full names the arguments' descriptions show; the plan refuses
  one that isn't. A child's state written before its parent took the
  parent's full name holds the bare id; the upgrade rewrites it too.
- **Domain names.** A hosted zone's or a record set's `dns_name` is the same
  name in any case: the API answers it in lowercase, and the state keeps it
  as configured, so neither plans a change. Names in a record set's values,
  such as a CNAME's target, are written in lowercase, as the API answers
  them.
- **Public zones.** A public zone is `PENDING_VERIFICATION` until it proves
  control of its domain: create the TXT record its `challenge` names
  (`challenge.dns_name`, holding `challenge.token`) where the domain is
  hosted now, with any provider. The platform checks pending zones on its
  own, and a refresh then finds the zone `ACTIVE`; Terraform doesn't wait
  for it. A zone that doesn't verify within 7 days is deleted with its
  record sets, and the next apply creates it again, with a new token.
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
