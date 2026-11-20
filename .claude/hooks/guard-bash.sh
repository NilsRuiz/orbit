#!/usr/bin/env bash
# PreToolUse hook (Bash): blocks dangerous git/terraform commands deterministically.
# Permission rules are glob-based and easy to bypass with extra flags; this is the
# second, stricter layer. Exit 2 = block (stderr is shown to Claude).
set -uo pipefail

input=$(cat)

# Extract tool_input.command (jq if available, python3 otherwise).
if command -v jq >/dev/null 2>&1; then
  cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // empty')
elif command -v python3 >/dev/null 2>&1; then
  cmd=$(printf '%s' "$input" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("tool_input",{}).get("command",""))')
else
  echo "guard-bash: jq or python3 required, hook skipped" >&2
  exit 0
fi

[ -z "$cmd" ] && exit 0

block() {
  echo "BLOCKED by .claude/hooks/guard-bash.sh: $1" >&2
  exit 2
}

# Normalise whitespace for matching.
c=$(printf '%s' "$cmd" | tr -s '[:space:]' ' ')

# --- git ------------------------------------------------------------------------
if [[ "$c" =~ git[[:space:]].*--no-verify ]]; then
  block "--no-verify is forbidden: fix the failing hook instead."
fi

if [[ "$c" =~ git[[:space:]]+push ]]; then
  if [[ "$c" =~ (--force|--force-with-lease|[[:space:]]-f([[:space:]]|$)|[[:space:]]\+[^[:space:]]) ]]; then
    block "force-push is forbidden. Add new commits instead."
  fi
  if [[ "$c" =~ push[[:space:]]+[^[:space:]]+[[:space:]]+(HEAD:)?(main|master)([[:space:]]|$) ]]; then
    block "pushing to main is forbidden: open a pull request (/pr)."
  fi
fi

if [[ "$c" =~ git[[:space:]]+commit ]]; then
  branch=$(git -C "${CLAUDE_PROJECT_DIR:-.}" branch --show-current 2>/dev/null || true)
  if [[ "$branch" == "main" || "$branch" == "master" ]]; then
    block "committing on '$branch' is forbidden. Run 'git switch -c <type>/<desc>' as a separate command first."
  fi
fi

if [[ "$c" =~ git[[:space:]]+(add[[:space:]]+(-A|--all|\.)([[:space:]]|$)) ]]; then
  block "stage files explicitly (git add <paths>) after reviewing git status."
fi

# --- terraform / cloud ------------------------------------------------------------
if [[ "$c" =~ (terraform|tofu)[[:space:]].*(apply|destroy) || "$c" =~ make[[:space:]].*tf-(apply|destroy) ]]; then
  block "terraform apply/destroy is run by the human only. Prepare a plan instead."
fi

if [[ "$c" =~ gcloud[[:space:]].*[[:space:]](delete|destroy)([[:space:]]|$) ]]; then
  block "destructive gcloud commands are run by the human only."
fi

# --- secrets ----------------------------------------------------------------------
if [[ "$c" =~ (cat|less|more|head|tail|bat)[[:space:]].*(\.env|\.pem|\.key|\.creds|\.tfstate) ]]; then
  block "reading secret files is forbidden."
fi

exit 0
