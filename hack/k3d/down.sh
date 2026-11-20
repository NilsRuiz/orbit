#!/usr/bin/env bash
set -euo pipefail
k3d cluster list -o json | grep -o '"name":"orbit-[^"]*"' | cut -d'"' -f4 | xargs -r -n1 k3d cluster delete
