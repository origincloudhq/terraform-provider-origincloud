#!/usr/bin/env bash
#
# Covers scripts/credentials.sh against fake `aws` and `gh`, which keep the
# secret in a file and record what GitHub is sent:
#
#   - signing-key stores a key that signs with its passphrase: RSA 4096,
#     signing only, without an expiry, as the Terraform Registry needs.
#   - signing-key refuses to replace a stored key.
#   - github creates the environment for v* tags once, and sets the private
#     key and the passphrase (without a trailing newline), which import and
#     sign as a release uses them.
#   - public-key prints the public key, and no secret is printed anywhere.
#
# Needs gpg and jq. Takes a few seconds, most of it generating the key.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly REPO_ROOT
readonly SCRIPT="${REPO_ROOT}/scripts/credentials.sh"

work="$(mktemp -d)"
cleanup() {
  gpgconf --homedir "${work}/verify" --kill gpg-agent 2>/dev/null || true
  rm -rf "${work}"
}
trap cleanup EXIT
mkdir -p "${work}/bin" "${work}/gh" "${work}/runtime"

cat > "${work}/bin/aws" <<'EOF'
#!/usr/bin/env bash
store="${FAKE_DIR}/secret.json"
args="$*"
case "${args}" in
  *describe-secret*) exit 0 ;;
  *list-secret-version-ids*) if [[ -s "${store}" ]]; then echo 1; else echo 0; fi ;;
  *put-secret-value*) file="${args##*file://}"; cp "${file%% *}" "${store}" ;;
  *get-secret-value*) cat "${store}" ;;
  *) echo "fake aws: unexpected: $*" >&2; exit 1 ;;
esac
EOF
cat > "${work}/bin/gh" <<'EOF'
#!/usr/bin/env bash
dir="${FAKE_DIR}/gh"
echo "$*" >> "${dir}/calls"
case "$*" in
  "secret set "*) cat > "${dir}/$3" ;;
  *"--method PUT"*) cat > "${dir}/environment.json" ;;
  *"--method POST"*) touch "${dir}/tag-policy" ;;
  *deployment-branch-policies*) if [[ -f "${dir}/tag-policy" ]]; then echo 1; fi ;;
  *) echo "fake gh: unexpected: $*" >&2; exit 1 ;;
esac
EOF
chmod +x "${work}/bin/aws" "${work}/bin/gh"
export PATH="${work}/bin:${PATH}" FAKE_DIR="${work}" XDG_RUNTIME_DIR="${work}/runtime"

failures=0
fail() { echo "FAIL: $*"; failures=$((failures + 1)); }
run() { "${SCRIPT}" "$@" >> "${work}/output" 2>&1; }

run signing-key || fail "signing-key failed: $(cat "${work}/output")"
if run signing-key; then
  fail "signing-key replaced a stored key"
fi
run github || fail "github failed"
run github || fail "github failed the second time"
run public-key || fail "public-key failed"

secret="${work}/secret.json"
[[ "$(jq -r 'keys | join(",")' "${secret}")" == "fingerprint,passphrase,private_key,public_key,revocation_certificate" ]] \
  || fail "the secret's fields are $(jq -r 'keys | join(",")' "${secret}")"

[[ "$(grep -c -- '--method POST' "${work}/gh/calls")" == 1 ]] || fail "the v* tag rule wasn't added exactly once"
jq -e '.deployment_branch_policy.custom_branch_policies == true' "${work}/gh/environment.json" >/dev/null \
  || fail "the environment admits more than its own rules"
grep -q 'name=v\* -f type=tag' "${work}/gh/calls" || fail "the rule isn't for v* tags"

for name in GPG_PRIVATE_KEY GPG_PASSPHRASE; do
  [[ -s "${work}/gh/${name}" ]] || fail "${name} wasn't set"
done
[[ "$(tail -c1 "${work}/gh/GPG_PASSPHRASE" | od -An -c | tr -d ' ')" != '\n' ]] || fail "GPG_PASSPHRASE ends in a newline"

# The key as a release imports it: the private key with its passphrase.
export GNUPGHOME="${work}/verify"
mkdir -m 700 "${GNUPGHOME}"
gpg --batch --quiet --pinentry-mode loopback --passphrase-file "${work}/gh/GPG_PASSPHRASE" \
  --import "${work}/gh/GPG_PRIVATE_KEY" 2>/dev/null || fail "the synced private key doesn't import"
echo probe > "${work}/probe"
if ! gpg --batch --quiet --pinentry-mode loopback --passphrase-file "${work}/gh/GPG_PASSPHRASE" \
    --detach-sign --output "${work}/probe.sig" "${work}/probe" 2>/dev/null \
  || ! gpg --batch --quiet --verify "${work}/probe.sig" "${work}/probe" 2>/dev/null; then
  fail "the synced key doesn't sign with its passphrase"
fi
# pub:<validity>:<bits>:<algo>:<keyid>:<created>:<expires>:...:<capabilities>
read -r bits algo expires capabilities < <(gpg --batch --with-colons --list-keys \
  | awk -F: '$1 == "pub" { print $3, $4, ($7 == "" ? "never" : $7), $12; exit }')
[[ "${algo}" == 1 && "${bits}" == 4096 ]] || fail "the key is algorithm ${algo}, ${bits} bits, not RSA 4096"
[[ "${expires}" == never ]] || fail "the key expires (${expires})"
[[ "${capabilities}" == scSC ]] || fail "the key's capabilities are ${capabilities}, not signing only"
[[ "$(gpg --batch --with-colons --list-keys | awk -F: '$1 == "fpr" { print $10; exit }')" == "$(jq -r .fingerprint "${secret}")" ]] \
  || fail "the synced key isn't the stored one"

grep -q -- '-----BEGIN PGP PUBLIC KEY BLOCK-----' "${work}/output" || fail "public-key printed no public key"
if grep -q -F -e 'PRIVATE KEY' -e "$(cat "${work}/gh/GPG_PASSPHRASE")" "${work}/output"; then
  fail "a secret was printed"
fi
[[ -z "$(ls -A "${work}/runtime")" ]] || fail "the script left its scratch directory behind"

if [[ "${failures}" -gt 0 ]]; then
  echo "${failures} failure(s)"
  exit 1
fi
echo "ok - credentials.sh"
