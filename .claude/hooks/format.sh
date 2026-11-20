#!/usr/bin/env bash
# PostToolUse hook (Edit|Write): auto-format the file Claude just changed, so diffs never
# contain formatting noise. Never blocks (exit 0).
set -uo pipefail

input=$(cat)
if command -v jq >/dev/null 2>&1; then
  file=$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty')
elif command -v python3 >/dev/null 2>&1; then
  file=$(printf '%s' "$input" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("tool_input",{}).get("file_path",""))')
else
  exit 0
fi

[ -n "$file" ] && [ -f "$file" ] || exit 0

case "$file" in
  *.go)
    command -v gofmt >/dev/null 2>&1 && gofmt -w "$file"
    ;;
  *.tf | *.tfvars)
    command -v terraform >/dev/null 2>&1 && terraform fmt "$file" >/dev/null
    ;;
esac
exit 0
