#!/usr/bin/env bash
# Renders the manifests of one payload version (payload/versions.tsv) into a directory.
# Usage: hack/payload/render.sh <version> <out-dir>
set -euo pipefail

[ $# -eq 2 ] || { echo "usage: $0 <version> <out-dir>" >&2; exit 2; }
version=$1
out=$2
root=$(cd "$(dirname "$0")/../.." && pwd)

# The version becomes an OCI tag and is substituted with sed: keep it to plain semver.
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "invalid version: $version" >&2; exit 1; }

line=$(awk -F'\t' -v v="$version" '!/^#/ && $1 == v' "$root/payload/versions.tsv")
[ -n "$line" ] || { echo "unknown payload version: $version" >&2; exit 1; }
IFS=$'\t' read -r _ tag healthy <<<"$line"

[[ $tag =~ ^[0-9A-Za-z._-]+$ ]] || { echo "invalid podinfo tag for $version: $tag" >&2; exit 1; }
case $healthy in
  true) ready=/readyz ;;
  false) ready=/readyz-never ;; # does not exist: the pod never becomes ready
  *) echo "invalid healthy column for $version: $healthy" >&2; exit 1 ;;
esac

mkdir -p "$out"
for f in "$root"/payload/manifests/*.yaml; do
  sed -e "s|__VERSION__|$version|g" -e "s|__PODINFO_TAG__|$tag|g" -e "s|__READY_PATH__|$ready|g" \
    "$f" >"$out/$(basename "$f")"
done

if grep -rqE '__[A-Z_]+__' "$out"; then
  echo "unrendered placeholder in $out" >&2
  exit 1
fi
