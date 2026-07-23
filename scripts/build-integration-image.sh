#!/usr/bin/env bash
# Build a portable, self-verifying hook-service integration image ARTIFACT.
#
# Unlike the tag/approval-gated registry release (which builds and pushes a
# digest-pinned image to GHCR under a protected environment), this produces a
# LOCAL, credential-free OCI/docker archive that an adjacent consumer — for
# example the platform integration harness — can load and verify WITHOUT
# pulling from any registry and WITHOUT spending on any hosted service. It is
# an integration convenience only; the real release path stays registry
# digest-pinned (see docs/releasing.md and deploy/compose/compose.pull.yaml).
#
# It writes, under dist/integration/ by default:
#   hook-service-integration-<commit>.tar   the `docker save` archive
#   SHA256SUMS                               portable archive checksum
#   manifest.json                            image id, archive sha256, provenance
#
# The manifest is generated alongside the archive, so a consumer can verify the
# archive bytes (archiveSha256) before `docker load` and confirm the loaded
# image identity (imageId) after load. No secret is read or written.
#
# Requirements: docker (build + save + inspect), git, sha256sum or shasum.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
repository_root="$(cd -- "${script_dir}/.." && pwd)"
# shellcheck source=deploy/scripts/lib.sh
. "${repository_root}/deploy/scripts/lib.sh"

cd "${repository_root}"

hs_require docker git

image_repo="${HOOKSHIP_INTEGRATION_IMAGE_REPO:-hookship/hook-service}"
out_dir="${HOOKSHIP_INTEGRATION_OUT_DIR:-${repository_root}/dist/integration}"

version="${HOOKSHIP_INTEGRATION_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || printf 'integration')}"
commit=$(git rev-parse --short=12 HEAD 2>/dev/null || printf 'none')
build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)

tag="integration-${commit}"
image_ref="${image_repo}:${tag}"

hs_log "building ${image_ref} (version=${version} commit=${commit})"
docker build \
	--build-arg "VERSION=${version}" \
	--build-arg "COMMIT=${commit}" \
	--build-arg "BUILD_DATE=${build_date}" \
	--tag "${image_ref}" \
	--tag "${image_repo}:integration" \
	"${repository_root}"

image_id=$(docker image inspect --format '{{.Id}}' "${image_ref}")

mkdir -p "${out_dir}"
archive_name="hook-service-integration-${commit}.tar"
archive_path="${out_dir}/${archive_name}"
hs_log "saving OCI/docker archive -> ${archive_path}"
docker save --output "${archive_path}" "${image_ref}"

# Portable checksum of the archive bytes, written in its own directory so the
# consumer can `hs_sha256_check SHA256SUMS` from that directory.
( cd "${out_dir}" && hs_sha256 "${archive_name}" >SHA256SUMS )
archive_sha256=$(awk '{print $1}' "${out_dir}/SHA256SUMS")

cat >"${out_dir}/manifest.json" <<JSON
{
  "app": "hook-service",
  "kind": "integration-image-archive",
  "note": "Local, credential-free OCI/docker archive for adjacent integration. NOT the registry release; the real release path stays GHCR digest-pinned.",
  "imageRef": "${image_ref}",
  "imageId": "${image_id}",
  "archive": "${archive_name}",
  "archiveSha256": "${archive_sha256}",
  "version": "${version}",
  "commit": "${commit}",
  "buildDate": "${build_date}",
  "source": "https://github.com/hookship/hook-service"
}
JSON

hs_log "integration image artifact ready:"
printf '  image ref : %s\n' "${image_ref}"
printf '  image id  : %s\n' "${image_id}"
printf '  archive   : %s\n' "${archive_path}"
printf '  sha256    : %s\n' "${archive_sha256}"
printf '  manifest  : %s/manifest.json\n' "${out_dir}"
