#!/usr/bin/env bash
#
# Moves the provider to the newest API definitions and the newest Go SDK
# release: API_REVISION to the head of origincloudhq/api's main, go.mod to
# the newest go.origincloud.eu/sdk release unless it already names a newer
# version (a pseudo-version of an unreleased commit stays), then generates
# the code and docs again (scripts/generate.sh --write). The regeneration
# workflow (.github/workflows/regenerate.yml) runs it daily; run it by hand
# the same way.
#
# A change to the API reaches the SDK with the SDK's next release. Until then
# the generated code may use a package the SDK doesn't have, and generating
# the docs, which builds the provider, fails.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT
readonly API_REPOSITORY="https://github.com/origincloudhq/api.git"
readonly SDK_MODULE="go.origincloud.eu/sdk"

cd "${ROOT}"

revision="$(git ls-remote "${API_REPOSITORY}" refs/heads/main | cut -f1)"
[[ "${revision}" =~ ^[0-9a-f]{40}$ ]] || { echo "no main branch in ${API_REPOSITORY}" >&2; exit 1; }
echo "${revision}" > API_REVISION

go get "${SDK_MODULE}@upgrade"
go mod tidy
sdk="$(go list -m -f '{{.Version}}' "${SDK_MODULE}")"

echo "origincloudhq/api@${revision:0:12}, ${SDK_MODULE} ${sdk}"
scripts/generate.sh --write
