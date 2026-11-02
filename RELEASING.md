# Releasing

## A release

A `v*` tag runs `.github/workflows/release.yml`: goreleaser (`.goreleaser.yml`)
builds the archives for every platform and signs `SHA256SUMS` with the
provider's signing key. The Terraform Registry and the OpenTofu registry read
the release from here. A version is never moved or deleted once released: the
registries keep it.

`VERSION` holds the version the next release tags. Before v1, a minor version
may change the resources incompatibly, as the preview APIs themselves may.

### The automatic path

1. **Regeneration.** When the Go SDK releases (its release dispatches it), or
   daily, `regenerate.yml` opens or refreshes the pull request from the branch
   `regenerate`, and sets the next version in `VERSION` with
   `scripts/version.sh bump`: a minor version when `API_REVISION` moved, a
   patch for a new SDK or a change of the generator alone.
2. **Merge.** Once CI passes on that pull request's head commit,
   `merge-regeneration.yml` merges it, if every commit in it is the
   regeneration workflow's. A pull request someone added commits to waits for
   review.
3. **Tag.** Once CI passes on `main`, `tag.yml` tags `VERSION` if it has no tag
   yet (`scripts/version.sh unreleased`), which runs `release.yml`.

The merge and the tag use the regeneration App's token, whose ID is the
variable `REGENERATE_APP_ID` and whose key is the secret
`REGENERATE_APP_PRIVATE_KEY`: a merge or a tag pushed with the workflow token
would start no further workflow, and so no release. Without the App, step 1
still opens the pull request, and the rest is done by hand.

### By hand

A change outside a regeneration is released by setting `VERSION` in the same
pull request: `scripts/version.sh bump minor` or `scripts/version.sh bump
patch`. `tag.yml` tags it once the pull request is merged and CI passes on
`main`. Without the App, tag a commit on `main` whose CI passed with the
version `VERSION` names:

```sh
git tag v0.4.0 origin/main
git push origin v0.4.0
```

## The signing key

Every release's `SHA256SUMS` is signed with the provider's own GPG key. The
Terraform Registry and the OpenTofu registry check every release against the
public key they hold for the namespace `origincloudhq`, and Terraform and
OpenTofu check it again on `init`.

| What | Copy of record | Copies |
|---|---|---|
| Signing key | AWS Secrets Manager `origincloud/terraform-provider/signing-key` (`eu-central-1`), JSON `{fingerprint, private_key, public_key, passphrase, revocation_certificate}` | Secrets `GPG_PRIVATE_KEY`, `GPG_PASSPHRASE` of this repository's environment `release` |
| Public key | The secret's `public_key` | The Terraform Registry's signing keys for `origincloudhq`; the OpenTofu registry's key for `origincloudhq` |

The key is RSA 4096, signing only, for `Origin Cloud Terraform Provider
<release@origincloud.eu>`, and does not expire: the registries verify old
releases against it too. The Terraform Registry accepts RSA and DSA keys only.
Its passphrase is random and stored with it.

The environment `release` admits `v*` tags only, so a workflow run from a
branch never sees these secrets.

**Secrets Manager is the source; GitHub is a copy.** GitHub secrets cannot be
read back. Never set the `GPG_*` secrets by hand: change the Secrets Manager
value, then sync.

The secret is created empty by Origin Cloud's infrastructure code
(`terraform/packages`). Its value is written only by `scripts/credentials.sh`,
which never prints a secret or puts one on a command line, and removes its
scratch directory on exit. `scripts/credentials.test.sh` covers it against
fake `aws` and `gh`.

### Prerequisites

- `aws` authenticated against the account holding the secret, with rights on
  it. The script refuses to run if the secret doesn't exist.
- `gh` authenticated as an admin of this repository.
- `gpg` and `jq`.

### First-time setup

1. Generate the key and store it. This refuses if the secret already holds
   one:

   ```sh
   scripts/credentials.sh signing-key
   ```

2. Copy it here, creating the environment `release`:

   ```sh
   scripts/credentials.sh github
   ```

3. Give the registries the public key. This prints it and its fingerprint:

   ```sh
   scripts/credentials.sh public-key
   ```

   - **Terraform Registry:** signed in with GitHub as an admin of origincloudhq,
     User Settings → Signing Keys, namespace `origincloudhq`.
   - **OpenTofu registry:** the "Submit new Provider Signing Key" issue form in
     [`opentofu/registry`](https://github.com/opentofu/registry), namespace
     `origincloudhq`, provider `origincloud`. The submitter's membership of
     origincloudhq must be public. Issues created through the API or `gh` are
     closed unprocessed.

### If the key is compromised or lost

1. **Compromised:** remove the key from the Terraform Registry's signing keys
   at once, and ask the OpenTofu registry to remove it with an issue in
   `opentofu/registry`.
2. **Generate a replacement:** delete the secret and recreate it empty from
   the infrastructure code. Then run `signing-key`, `github` and `public-key`,
   and add the new public key to both registries as in step 3 above.
3. **Release a new version**, signed with the new key.

**Lost:** neither GitHub nor Secrets Manager holds the key any more. Follow
steps 2–3.

### Checking the setup

Neither command prints a secret:

```sh
aws secretsmanager list-secret-version-ids --region eu-central-1 \
  --secret-id origincloud/terraform-provider/signing-key --query 'Versions[].CreatedDate'
gh secret list --repo origincloudhq/terraform-provider-origincloud --env release
```
