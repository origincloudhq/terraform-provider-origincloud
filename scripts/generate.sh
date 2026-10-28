#!/usr/bin/env bash
#
# Generates the provider's resources and data sources
# (internal/services/<service><version>, internal/provider/services.gen.go)
# from the API definitions in origincloudhq/api at the commit API_REVISION
# names, with this repository's plugin
# (internal/cmd/protoc-gen-terraform-origincloud), over the Go SDK's
# packages. Then it generates the registry docs (docs/) from the provider's
# schema and examples/, with tfplugindocs.
#
#   scripts/generate.sh           # check the checked-in code and docs are current
#   scripts/generate.sh --write   # generate them again
#
# Needs go, git and network access; buf and tfplugindocs run through `go run`
# at the versions below, and tfplugindocs downloads the Terraform it reads the
# schema with.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT
readonly API_REPOSITORY="https://github.com/origincloudhq/api.git"
# The Go SDK's module, whose packages the generated code uses.
readonly SDK_MODULE="go.origincloud.eu/sdk"
readonly BUF="github.com/bufbuild/buf/cmd/buf@v1.73.0"
readonly TFPLUGINDOCS="github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0"
readonly TF_VERSION="${TF_ACC_TERRAFORM_VERSION:-1.16.2}"

revision="$(tr -d '[:space:]' < "${ROOT}/API_REVISION")"
[[ "${revision}" =~ ^[0-9a-f]{40}$ ]] || { echo "API_REVISION must hold a full commit hash" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

git init --quiet "${tmp}/api"
git -C "${tmp}/api" fetch --quiet --depth 1 "${API_REPOSITORY}" "${revision}"
git -C "${tmp}/api" checkout --quiet FETCH_HEAD
(cd "${ROOT}" && go build -o "${tmp}/bin/protoc-gen-terraform-origincloud" ./internal/cmd/protoc-gen-terraform-origincloud)

# Each file's Go package in the SDK: origincloud/<service>/<version> is
# go.origincloud.eu/sdk/<service>/<version>, package <service><version>.
opts=''
while read -r file; do
  dir="$(dirname "${file}")"
  rel="${dir#origincloud/}"
  opts+="${opts:+,}\"M${file}=${SDK_MODULE}/${rel};${rel//\//}\""
done < <(cd "${tmp}/api" && find origincloud -name '*.proto' | LC_ALL=C sort)

# One run over every file: the plugin also writes the provider's list of
# packages.
template="$(printf '{"version":"v2","plugins":[{"local":"%s","out":"%s","strategy":"all","opt":[%s]}]}' \
  "${tmp}/bin/protoc-gen-terraform-origincloud" "${tmp}/gen" "${opts}")"
(cd "${ROOT}" && go run "${BUF}" generate "${tmp}/api" --template "${template}")

# The generated files, relative to the repository.
generated() { (cd "$1" && find . -name '*.gen.go' -not -path './dist/*' | LC_ALL=C sort); }

# Writes the provider in $1's docs/, and checks their layout. The copy
# checked below is outside any git repository, so the build stamps no VCS
# information.
docs() {
  (cd "$1" && export GOFLAGS="${GOFLAGS:+${GOFLAGS} }-buildvcs=false" \
    && go run "${TFPLUGINDOCS}" generate --provider-name origincloud --tf-version "${TF_VERSION}" >/dev/null \
    && { out="$(go run "${TFPLUGINDOCS}" validate --provider-name origincloud 2>&1)" || { echo "${out}" >&2; false; }; })
}

if [[ "${1:-}" == "--write" ]]; then
  generated "${ROOT}" | (cd "${ROOT}" && xargs -r rm -f)
  (cd "${tmp}/gen" && find . -name '*.gen.go' -exec cp --parents {} "${ROOT}" \;)
  echo "wrote $(generated "${ROOT}" | wc -l) files from origincloudhq/api@${revision:0:12}"
  docs "${ROOT}"
  echo "wrote docs/"
  exit 0
fi

failed=0
if ! diff <(generated "${ROOT}") <(generated "${tmp}/gen") >/dev/null; then
  failed=1
else
  while read -r file; do
    cmp -s "${ROOT}/${file}" "${tmp}/gen/${file}" || failed=1
  done < <(generated "${ROOT}")
fi
if [[ "${failed}" -eq 1 ]]; then
  echo "FAILED: the generated code isn't what origincloudhq/api@${revision:0:12} generates; run $0 --write"
  exit 1
fi

# The docs are generated in a copy, so that a stale page shows as a diff.
mkdir "${tmp}/provider"
(cd "${ROOT}" && tar --exclude=./.git --exclude=./dist --exclude=./docs -cf - .) | tar -C "${tmp}/provider" -xf -
docs "${tmp}/provider"
if ! diff -r "${ROOT}/docs" "${tmp}/provider/docs" >/dev/null 2>&1; then
  echo "FAILED: docs/ isn't what the schema and examples generate; run $0 --write"
  exit 1
fi
echo "ok - the generated code and docs are current (origincloudhq/api@${revision:0:12})"
