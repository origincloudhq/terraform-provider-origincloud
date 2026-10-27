#!/usr/bin/env bash
#
# Creates and distributes the provider's release signing key. Run by an
# operator from a workstation; nothing runs this in CI. The procedure around
# it is RELEASING.md.
#
#   scripts/credentials.sh signing-key
#   scripts/credentials.sh github
#   scripts/credentials.sh public-key
#
# AWS Secrets Manager holds the copy of record:
#
#   origincloud/terraform-provider/signing-key
#       {fingerprint, private_key, public_key, passphrase, revocation_certificate}
#
# The secret is created, empty, by Origin Cloud's infrastructure code
# (terraform/packages). This repository's GPG_* secrets are copies, written
# by `github` from Secrets Manager - never the other way round, and never
# typed by hand.
#
# No secret is printed, passed on a command line (argv is visible in `ps`), or
# written outside a private temporary directory that is removed on exit.
#
# Needs: aws (authenticated against the account holding the secret), gh
# (authenticated with admin on this repository), gpg, jq.

set -euo pipefail

readonly REGION="${TERRAFORM_PROVIDER_AWS_REGION:-eu-central-1}"
readonly SECRET="origincloud/terraform-provider/signing-key"
readonly REPO="${TERRAFORM_PROVIDER_GITHUB_REPO:-origincloudhq/terraform-provider-origincloud}"
# The secrets are the environment's, which only v* tags may deploy to: a
# workflow run from a branch never sees them.
readonly ENVIRONMENT="release"
readonly KEY_UID="Origin Cloud Terraform Provider <release@origincloud.eu>"
# RSA: the Terraform Registry accepts RSA and DSA keys only, not ed25519 like
# the CLI's release key. The key doesn't expire: the registries verify every
# release against the key they hold, old releases included, so an expiry
# would stop installs of all of them at once.
readonly KEY_ALGO="rsa4096"
readonly KEY_EXPIRY="never"

fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
log()  { printf '%s\n' "$*"; }

for tool in aws gh gpg jq; do
  command -v "${tool}" >/dev/null || fail "${tool} is required"
done

# Private scratch space: tmpfs where available, mode 0700, removed (and its
# files overwritten first) on every exit path.
umask 077
scratch="$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/terraform-provider-credentials.XXXXXX")"
cleanup_hooks=()
cleanup() {
  local hook
  for hook in "${cleanup_hooks[@]}"; do eval "${hook}" || true; done
  find "${scratch}" -type f -exec shred -u {} + 2>/dev/null || true
  rm -rf "${scratch}"
}
trap cleanup EXIT INT TERM

aws_() { aws --region "${REGION}" "$@"; }

secret_exists() {
  aws_ secretsmanager describe-secret --secret-id "${SECRET}" >/dev/null 2>&1 \
    || fail "secret ${SECRET} does not exist - apply terraform/packages first"
}

# Whether the secret already holds a value (any version).
secret_has_value() {
  local n
  n="$(aws_ secretsmanager list-secret-version-ids --secret-id "${SECRET}" --query 'length(Versions)' --output text)"
  [[ "${n}" != 0 && "${n}" != None ]]
}

# Reads the secret's current value into a file.
get_secret() {
  aws_ secretsmanager get-secret-value --secret-id "${SECRET}" --query SecretString --output text > "$1"
  [[ -s "$1" ]] || fail "secret ${SECRET} has no value - run signing-key first"
}

# --- signing-key --------------------------------------------------------------

cmd_signing_key() {
  secret_exists
  if secret_has_value; then
    fail "${SECRET} already holds a signing key. Replacing it is a key rotation: the registries verify every release against it. Follow RELEASING.md instead of re-running this."
  fi

  export GNUPGHOME="${scratch}/gnupg"
  mkdir -m 700 "${GNUPGHOME}"
  cleanup_hooks+=("gpgconf --homedir '${GNUPGHOME}' --kill gpg-agent")

  head -c 48 /dev/urandom | base64 | tr -d '\n' > "${scratch}/passphrase"

  log "generating an ${KEY_ALGO} signing key for '${KEY_UID}', expiring ${KEY_EXPIRY}"
  gpg --batch --quiet --pinentry-mode loopback --passphrase-file "${scratch}/passphrase" \
      --quick-generate-key "${KEY_UID}" "${KEY_ALGO}" sign "${KEY_EXPIRY}"

  local fpr
  fpr="$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr"{print $10; exit}')"
  [[ -n "${fpr}" ]] || fail "key generation produced no key"

  gpg --batch --quiet --pinentry-mode loopback --passphrase-file "${scratch}/passphrase" \
      --armor --export-secret-keys "${fpr}" > "${scratch}/private.asc"
  gpg --batch --armor --export "${fpr}" > "${scratch}/public.asc"
  local rev="${GNUPGHOME}/openpgp-revocs.d/${fpr}.rev"
  [[ -s "${rev}" ]] || fail "gpg did not write a revocation certificate at ${rev}"

  # A key that cannot sign with its own passphrase is worth nothing; check
  # before storing it. The registries read a binary detached signature, as
  # the release writes one.
  printf 'probe\n' > "${scratch}/probe"
  gpg --batch --quiet --pinentry-mode loopback --passphrase-file "${scratch}/passphrase" \
      --local-user "${fpr}" --detach-sign --output "${scratch}/probe.sig" "${scratch}/probe"
  gpg --batch --quiet --verify "${scratch}/probe.sig" "${scratch}/probe" 2>/dev/null \
    || fail "the generated key does not produce a verifiable signature"

  jq -n --arg fingerprint "${fpr}" \
        --rawfile private_key "${scratch}/private.asc" \
        --rawfile public_key "${scratch}/public.asc" \
        --rawfile passphrase "${scratch}/passphrase" \
        --rawfile revocation_certificate "${rev}" \
        '{fingerprint: $fingerprint, private_key: $private_key, public_key: $public_key, passphrase: $passphrase, revocation_certificate: $revocation_certificate}' \
        > "${scratch}/signing.json"
  aws_ secretsmanager put-secret-value --secret-id "${SECRET}" --secret-string "file://${scratch}/signing.json" >/dev/null

  log "stored signing key ${fpr} in ${SECRET}"
  log "Next: scripts/credentials.sh github"
}

# --- github -------------------------------------------------------------------

# Sets an environment secret from a file; gh reads the value from stdin.
gh_secret() {
  gh secret set "$1" --repo "${REPO}" --env "${ENVIRONMENT}" < "$2" >/dev/null
  log "  secret ${1} (environment ${ENVIRONMENT})"
}

cmd_github() {
  get_secret "${scratch}/signing.json"

  # `-j`, not `-r`: no trailing newline, which a passphrase must not have.
  local field
  for field in private_key passphrase; do
    jq -ej ".${field}" "${scratch}/signing.json" > "${scratch}/${field}" \
      || fail "${SECRET} has no ${field}"
  done

  log "syncing ${REPO} from Secrets Manager"
  # The environment, deployable from v* tags only. Both calls are idempotent:
  # PUT replaces the environment's settings, and the tag rule is added only
  # when it is missing.
  gh api --method PUT "repos/${REPO}/environments/${ENVIRONMENT}" --input - >/dev/null <<'EOF'
{"deployment_branch_policy": {"protected_branches": false, "custom_branch_policies": true}}
EOF
  if ! gh api "repos/${REPO}/environments/${ENVIRONMENT}/deployment-branch-policies" \
      --jq '.branch_policies[] | select(.type == "tag" and .name == "v*") | .id' | grep -q .; then
    gh api --method POST "repos/${REPO}/environments/${ENVIRONMENT}/deployment-branch-policies" \
      -f name='v*' -f type=tag >/dev/null
  fi
  log "  environment ${ENVIRONMENT}, v* tags only"

  gh_secret GPG_PRIVATE_KEY "${scratch}/private_key"
  gh_secret GPG_PASSPHRASE "${scratch}/passphrase"
  log "done - signing key $(jq -r .fingerprint "${scratch}/signing.json")"
}

# --- public-key ---------------------------------------------------------------

# Prints the public key and its fingerprint, for the registries.
cmd_public_key() {
  get_secret "${scratch}/signing.json"
  jq -ej .public_key "${scratch}/signing.json" || fail "${SECRET} has no public_key"
  log ""
  log "fingerprint $(jq -r .fingerprint "${scratch}/signing.json")"
}

case "${1:-}" in
  signing-key) cmd_signing_key ;;
  github)      cmd_github ;;
  public-key)  cmd_public_key ;;
  *) fail "usage: $0 signing-key | github | public-key" ;;
esac
