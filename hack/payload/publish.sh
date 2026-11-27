#!/usr/bin/env bash
# Publishes every payload version of payload/versions.tsv as an OCI artifact, then signs
# it with cosign (keyless in CI). Versions are immutable: a published version is skipped
# if unchanged, and the script fails if its manifests changed.
#
# Usage: hack/payload/publish.sh <oci-repository> [--dry-run]
#   e.g. hack/payload/publish.sh ghcr.io/nilsruiz/orbit-payload
# Needs: flux, and cosign unless ORBIT_PAYLOAD_SIGN=false. Registry login beforehand.
set -euo pipefail

[ $# -ge 1 ] || { echo "usage: $0 <oci-repository> [--dry-run]" >&2; exit 2; }
repo=$1
dry_run=${2:-}
sign=${ORBIT_PAYLOAD_SIGN:-true}
root=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

source_url=$(git -C "$root" config --get remote.origin.url || echo unknown)
commit=$(git -C "$root" rev-parse HEAD)

awk -F'\t' '!/^#/ && NF { print $1 }' "$root/payload/versions.tsv" | while read -r version; do
  dir="$work/$version"
  "$root/hack/payload/render.sh" "$version" "$dir"
  ref="oci://${repo}:${version}"

  if [ "$dry_run" = --dry-run ]; then
    echo "rendered $version"
    continue
  fi

  mkdir -p "$work/pulled-$version"
  if pull_err=$(flux pull artifact "$ref" --output "$work/pulled-$version" 2>&1); then
    # Published already: same manifests, or refuse (a rollout may be running it).
    if flux diff artifact "$ref" --path "$dir" >/dev/null 2>&1; then
      echo "unchanged $version"
      continue
    fi
    echo "payload $version is already published with different manifests: versions are" \
      "immutable, add a new version to payload/versions.tsv" >&2
    flux diff artifact "$ref" --path "$dir" >&2 || true
    exit 1
  fi
  if ! grep -qiE 'not found|manifest unknown|MANIFEST_UNKNOWN|NAME_UNKNOWN' <<<"$pull_err"; then
    echo "cannot check whether $version is published: $pull_err" >&2
    exit 1
  fi

  digest_ref=$(flux push artifact "$ref" --path "$dir" --source "$source_url" \
    --revision "${version}@sha1:${commit}" --output json |
    sed -nE 's|.*"url": *"oci://([^"]+)".*|\1|p')
  [[ $digest_ref == *@sha256:* ]] || { echo "push of $version returned no digest" >&2; exit 1; }
  echo "pushed $version ($digest_ref)"

  if [ "$sign" = true ]; then
    cosign sign --yes "$digest_ref"
  fi
done
