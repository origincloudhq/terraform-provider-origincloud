# Contributing

Thank you for helping improve Origin Cloud's provider for Terraform and
OpenTofu.

## Issues

Open an issue for anything that is wrong, unclear or missing: an attribute
that behaves differently from its description, a plan that changes when nothing
did, a resource you need. Say which resource or data source it is about, and
include the configuration and the output of `terraform plan` or `apply` where
you can, with tokens and account details removed.

To report a security vulnerability, don't open an issue: use **Report a
vulnerability** on this repository's Security tab, which reaches us privately.

## Pull requests

Pull requests are reviewed and merged here. For anything larger than a small
fix, open an issue first, so that we can agree on the change before you spend
time on it. CI must pass: the checks below, run on every pull request.

## How the provider is built

The resources and data sources (`internal/services/<service><version>`) are
generated from the API definitions in
[origincloudhq/api](https://github.com/origincloudhq/api), at the commit
`API_REVISION` names, by this repository's plugin,
`internal/cmd/protoc-gen-terraform-origincloud`, over the
[Go SDK](https://github.com/origincloudhq/sdk-go)'s packages.
`internal/framework` (the Terraform side of the standard methods) and
`internal/provider` are written by hand. So a change to what a resource
offers starts in the API; a change to how every resource behaves is a change
to the plugin or `internal/framework`.

- A resource message the service can `Get` becomes a **resource** when the
  service can also `Create` and `Delete` it, and a **data source** otherwise.
  A create without a delete fails generation, unless the type is created
  outside Terraform (below). Its name's variables before its own
  (`project`, `zone`) are required attributes that replace it. A resource
  type with several patterns, such as a project's images and the public
  ones, is created in the pattern whose parent its create's HTTP binding
  takes (`{parent=projects/*}`): without such a binding, or with one no
  pattern matches, generation fails. It also gets a data source of the
  same name, which reads one in any pattern by its resource name.
- A create that takes the id the client chooses (AIP-133), such as
  `CreateProject`'s `project_id`, makes it an attribute that replaces the
  resource: required when the field is `REQUIRED`, optional and computed
  when it is `OPTIONAL`. It is the last segment of the resource's name, which
  is how it is read back and how an import derives it. A deleted resource's
  id can't be used again, so a plan that would replace the resource with the
  id it has, because an immutable top-level attribute changed, or a oneof
  switched away from an immutable member, fails with a message asking for a
  new id. Moving it to another parent doesn't count: its new name is
  another. The guard doesn't see an immutable field nested under a mutable
  one, a replacement forced from the command line (`-replace`), or one of a
  tainted resource: Terraform decides those, not the provider.
- A **singleton**, a resource its parent has one of, such as a project's
  quota (`projects/{project}/quota`), that the service gets and updates but
  doesn't create or delete, is a resource too. Creating it updates it, with
  the fields the configuration sets. Terraform owns a field once it has sent
  it, recorded in the resource's private state after the API accepted it,
  so a failed update changes nothing it owns. A field it owns that the
  configuration no longer sets is unset, planned unknown, as the API may
  answer with no value or fall back to a default, which the state then
  keeps; destroying the resource unsets the fields it owns and leaves the
  others. A field set to the value the API already has, as after an import,
  isn't sent, so it isn't owned until a change sends it. `REQUIRED` fields
  are sent but never owned or unset. Its `OPTIONAL` fields need explicit
  presence, so that one left out keeps its value. A repeated or map field
  can't have it, but is optional and computed the same way, unset meaning
  empty: set to `[]` or `{}`, it is sent empty, and so is one it owns that
  the configuration no longer sets. A singleton with a create fails
  generation. A singleton the service doesn't update is a data source.
- An imported name must have the resource's pattern.
- A field referring to another resource (`google.api.resource_reference`) is
  a string the API checks, so a reference that may take several patterns
  needs nothing of its own.
- A resource type the service also lists (`List<plural>`, AIP-132) gets a
  second data source named after its plural, such as
  `origincloud_pvs_volumes`, that reads every page of the list. The name's
  variables before the resource's own are its required attributes; each
  item has the data source's attributes, all computed, except secrets. A
  required field of the list's request, such as the organization projects
  are listed in, is a required attribute, if it is a string, number, boolean
  or enum; its other fields, such as a filter, are left unset. Items in a
  terminal state are left out.
- `REQUIRED` is required, `OUTPUT_ONLY` computed, `IMMUTABLE` replaces the
  resource when it changes. An `OPTIONAL` `IMMUTABLE` field is also computed,
  keeping the value the API chose when it was left out (the default storage
  class); a mutable `OPTIONAL` field left out stays null.
- Creates, updates and deletes wait on their operation; a create, update or
  delete that answers with the resource, or with nothing, is done when it
  answers. A resource whose create failed after it existed (the operation's
  metadata names it) is kept as tainted. So is a resource with a secret
  whose create was sent again with its request id after the answer was
  lost: the API refuses the repeat with `CONFLICT`, naming the resource in
  the error's metadata, as only the first answer carries the secret. Only
  where the API chose its name: a `CONFLICT` naming one whose id the client
  chooses may be about someone else's.
- A resource not found, or whose `state` is a terminal value, `DELETED` or
  `TERMINATED`, is gone: it leaves the state at a refresh, isn't deleted
  again, and a delete that fails while it goes there succeeds.
- Fields of these kinds are supported, settable or computed:
  - strings, integers, booleans and enums; a settable enum takes its
    values' names only;
  - nested messages, as nested attributes (`SingleNestedAttribute`). Their
    fields follow the same field behaviours, except that a nested message has
    no identifier;
  - repeated fields of those, as lists, or as sets when the field is
    `UNORDERED_LIST` (the API doesn't keep its order): `ListAttribute` and
    `SetAttribute` of scalars, `ListNestedAttribute` and `SetNestedAttribute`
    of messages. A list is compared in order, so a reordered list is a
    change; a set isn't;
  - maps of strings to strings, or to messages (`MapNestedAttribute`).
- A nested message or a repeated field is sent whole. An update names, in its
  mask, the top-level field holding it when anything it sends changed;
  output-only fields within it don't count.
- `IMMUTABLE` on a field holding nested messages replaces the resource when
  what it sends changes, so its output-only fields don't. `IMMUTABLE` inside
  a nested message replaces the resource when that field changes, and an
  `OPTIONAL` `IMMUTABLE` one keeps the value the API chose; when the object
  holding it is new, the value is unknown until the API answers.
- An object the API chooses when it is left out (`IMMUTABLE`, not `REQUIRED`)
  has only optional and computed fields in the schema: left out, Terraform
  plans each field from the configuration unless it is computed, so a
  required one would plan a change at every run. The API checks them.
- An optional attribute left out stays null when the API answers with
  nothing, and one set to `[]` or `{}` stays as it was set. A computed list,
  set or map is empty, not null; a computed object the API leaves out or
  answers with all its fields zero is null. A null element is refused, in
  maps of strings too.
- Any other field fails generation with its name and the reason, so a new
  API that uses one extends the plugin first:
  - other scalars, such as `repeated double fields aren't supported yet`;
  - maps keyed by anything but strings (`only maps keyed by strings are
    supported`), or of strings to other values (`maps of strings to int32
    aren't supported yet, only to strings and messages`);
  - another package's message (`google.longrunning.Operation is another
    package's message, which isn't supported yet`), a google.protobuf
    message other than `Timestamp` and `Duration` (`google.protobuf.Struct
    fields aren't supported yet, of google.protobuf only Timestamp and
    Duration`), repeated or mapped timestamps and durations (`repeated
    google.protobuf.Timestamp fields aren't supported yet`), and a message
    holding itself (`… holds itself, which isn't supported`);
  - in the elements of a settable `UNORDERED_LIST` field, an `OUTPUT_ONLY`
    field (`a field of an UNORDERED_LIST element can't be OUTPUT_ONLY`): the
    element the API answered with would differ from the one set, and a set
    has no order to match them up by;
  - in the elements of a settable repeated or map field, at any depth, an
    `IMMUTABLE` field (`a field of a repeated or map field's elements can't be
    IMMUTABLE: an element has no identity to keep its value for, or to
    replace the resource on`): elements are matched by position or key, so a
    kept value could be planned for another element, and adding or
    reordering elements would replace the resource. The elements of an
    immutable list or map may have one, since any change to them replaces
    the resource anyway; a set's never, as an element whose value the API
    chose would differ from the one set;
  - `UNORDERED_LIST` on a field that isn't repeated, and an `IDENTIFIER` in a
    nested message.
- A `google.protobuf.Timestamp` is a string in RFC 3339, such as
  `2026-11-01T12:00:00Z`, and a `google.protobuf.Duration` a string in
  seconds, such as `90s`: their JSON forms. Unset is null, a settable one is
  validated, and a value written differently, in another time zone, is kept
  while it is the same instant or length. An update names it, and an
  immutable one replaces the resource, only when the instant or length
  changes.
- A field with explicit presence (proto3 `optional`) that is `OPTIONAL` is
  optional and computed: left out, the API chooses it, and it can still be
  changed; removing it from the configuration keeps the current value
  rather than clearing it. Unset is null. A message field marked `optional`
  is an object the API may choose, the same way. Neither may be in a list's
  elements, which are matched by position, unless the list is immutable (`a
  field with explicit presence (proto3 optional) can't be in a list's
  elements`); a set's and a map's elements may have them.
- A oneof's members are attributes of their own, of which at most one may be
  set; exactly one when its members are `REQUIRED`. A member that isn't the
  one the oneof holds is null. A member the state keeps, such as an
  immutable one the API chose, is planned null while the configuration
  sets another, so that the other is sent. Switching away from an
  `IMMUTABLE` member, or to one, replaces the resource; switching between
  mutable members updates it, naming both in the mask.
- A boolean that a pair of custom methods sets and clears, rather than an
  update, is a **toggle**, such as a snapshot's `protected`
  (`ProtectSnapshot`, `UnprotectSnapshot`). Nothing in the API marks such a
  pair, so each is declared in the plugin's `toggles.go`, which generation
  checks against the API. The methods answer with operations, which are
  waited on.
- An output-only string the API answers with only in the create's response,
  such as a generated private key, is a **secret**: sensitive, and kept from
  the create on, though the API never answers with it again. Nothing in the
  API marks such a field either, so each is declared in `secrets.go`, which
  generation checks against the API. A secret of a package the API doesn't
  have yet waits for it, so that it is declared before its service is
  published, and the service's first regeneration has it from the start.
  An imported resource has none, and
  data sources leave it out: no read answers with it.
- A required, immutable string the API answers in a form of its own, such
  as a role binding's `member` (`user:Mail@Example.com` is answered as
  `user:mail@example.com`, and later with the user's new address), is
  **spelled**: the state keeps the value configured while the API's answer
  names the same thing, as the framework's `Spelling` for it says, and
  records the answer in the resource's private state, so that a
  configuration changed to it doesn't replace the resource either; only a
  value that names something else does. Nothing in the API marks such a
  field, so each is declared in `spellings.go`, which generation checks
  against the API, as it does secrets.
- A resource type the API creates but never deletes, such as a support
  case, which is closed instead, is **created outside Terraform**: as a
  resource, Terraform could create it but never destroy it, so its create
  is ignored, and it is a data source, with its plural's when the service
  lists it. Nothing in the API marks such a type, so each is declared in
  `outside.go`, with the reason, which generation checks against the API,
  as it does secrets: one the API doesn't create, or deletes, fails it.
- Every method of a service is called by a generated resource or data
  source, or is **not managed**, such as a query over a window of time
  series, whose answer would change at every plan. Each method not managed
  is declared in `methods.go`, with the reason. A method neither called nor
  declared fails generation, so a method the API adds is never left out
  unnoticed. A declaration the API doesn't have, or that something calls,
  fails it too.

The plugin's tests include a test API, `internal/testapi/firewalls.prototext`,
with a firewall, whose fields are nested messages, lists, sets and maps;
a key, created synchronously, with timestamps, durations, fields with
explicit presence, oneofs, a toggle, a secret and a terminal state; and a
vault, whose id the client chooses, with a singleton quota, whose fields
have presence or are repeated, and tickets, created outside Terraform. The
tests check that its generated code in `internal/testapi` is current, and run
Terraform against a fake of it. After changing the plugin or the test API,
generate that code again:

```sh
go test ./internal/cmd/protoc-gen-terraform-origincloud -run TestTestAPI -update
```

The registry docs in `docs/` are generated by tfplugindocs from the schema and
`examples/`. Edit the examples or the API's descriptions, never `docs/`.

The regeneration workflow (`.github/workflows/regenerate.yml`) moves the
provider to the newest API and Go SDK release when the SDK releases, and
daily, and opens a pull request when that changes anything, with the next
version. Once its CI passes, it is merged and released without anyone's
hands, unless someone added commits to it, or it adds a service, which needs tests
against a fake of it and its rows in README.md's table first: these wait for
review ([RELEASING.md](RELEASING.md)).

## Checks

Go and network access are all you need; the tools run through `go run`, and
the tests download Terraform (`TF_ACC_TERRAFORM_VERSION`, or the one on the
`PATH`).

```sh
scripts/generate.sh --write   # generate the code and docs again; without --write, check them
scripts/update.sh             # move to the newest API and SDK, and generate
go test ./...                 # runs Terraform against fakes of each service and the test API
ORIGINCLOUD_LIVE_ZONE=projects/<project>/zones/<zone> \
  go test -count=1 -v -run TestLive ./internal/provider   # against the live API
```

The live tests only read, except `TestLivePVS`, which creates and destroys
volumes and snapshots in the zone's project, and `TestLiveDNS`, which
creates and destroys hosted zones, a record set, a VPC and a private zone's
attachment to it. Their credentials need
`roles/owner` on that project, and `roles/viewer` and `roles/billing.viewer`
on its organization, to list its projects, billing accounts and support cases.

CI also checks formatting (`gofmt`), `go mod tidy`, `go vet`, the release
build and the scripts. Releases are described in [RELEASING.md](RELEASING.md).

## License

By contributing, you agree that your contribution is licensed under the
Mozilla Public License 2.0, like the rest of this repository.
