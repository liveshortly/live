#!/usr/bin/env bash
# harness-matrix.sh <agent> — LSH-00 headless matrix row (steps 1, 2 and 8).
#
# Runs one agent headless through `live` against a LOCAL LiveShortly stack, then
# reads the session back and prints a HARNESS-MATRIX.md row. Steps 3–7, 9, 10
# are manual: see docs/HARNESS-TESTING.md.
#
# Preconditions (docs/HARNESS-TESTING.md §1–3):
#   - local stack up on http://localhost:8080
#   - an isolated HOME: export HOME=$(mktemp -d); make use-local; ./bin/live login
#   - LIVE=<path to the live binary under test> (default: ./bin/live)
#
# Every run uses the agent's own account/quota — except ollama with a local
# model. The scratch repo is /tmp/lsh-repo (override with LSH_REPO). Agents run
# with their own permission prompts bypassed (headless; nobody to answer) inside
# that scratch repo only. Override an agent's argv with LSH_CMD="…".
set -euo pipefail

agent="${1:?usage: harness-matrix.sh <claude|codex|agy|gemini|ollama|aider|cursor-agent|hermes|qwen>}"
LIVE="${LIVE:-$PWD/bin/live}"
REPO="${LSH_REPO:-/tmp/lsh-repo}"
PROMPT="Read README.md, add a line 'hello from $agent', then run \`ls\`."

# Never against production: host.json must exist in this HOME and point at localhost.
host="$HOME/.liveshortly/host.json"
api="$(jq -r '.api_url // empty' "$host" 2>/dev/null || true)"
case "$api" in
  http://localhost:*|http://127.0.0.1:*) ;;
  *) echo "refusing: $host must point at the local stack (got '${api:-none}'). See docs/HARNESS-TESTING.md" >&2; exit 2 ;;
esac
[ -x "$LIVE" ] || { echo "no live binary at $LIVE (make build, or set LIVE=)" >&2; exit 2; }

if [ ! -d "$REPO/.git" ]; then
  mkdir -p "$REPO" && git -C "$REPO" init -q && printf '# lsh-repo\n' > "$REPO/README.md"
  git -C "$REPO" add README.md && git -C "$REPO" -c user.name=lsh -c user.email=lsh@localhost commit -qm init
fi

if [ -n "${LSH_CMD:-}" ]; then
  read -r -a argv <<< "$LSH_CMD"
else
  case "$agent" in
    claude)       argv=(claude -p "$PROMPT" --dangerously-skip-permissions) ;;
    codex)        argv=(codex exec --dangerously-bypass-approvals-and-sandbox "$PROMPT") ;;
    agy)          argv=(agy -p "$PROMPT" --dangerously-skip-permissions) ;;
    gemini)       argv=(gemini -p "$PROMPT" --yolo) ;;
    ollama)       argv=(ollama run "${LSH_OLLAMA_MODEL:-phi4-mini}" "In one short sentence, say hello from ollama.") ;;
    aider)        argv=(aider --yes-always --no-auto-commits --message "$PROMPT") ;;
    cursor-agent) argv=(cursor-agent -p "$PROMPT" --force) ;;
    *) echo "no default argv for '$agent'; set LSH_CMD" >&2; exit 2 ;;
  esac
fi

version="$("${argv[0]}" --version 2>/dev/null </dev/null | head -1 || true)"
start=$(date +%s)
( cd "$REPO" && "$LIVE" "${argv[@]}" ) || echo "($agent exited $?)" >&2
took=$(( $(date +%s) - start ))

id="$("$LIVE" ls -n 1 --json | jq -r '.results[0].id // empty')"
[ -n "$id" ] || { echo "no session found" >&2; exit 1; }
sleep 2 # let the final events and the stop land
info="$("$LIVE" info "$id" --json)"

title="$(jq -r .title <<<"$info")"
status="$(jq -r .status <<<"$info")"
model="$(jq -r '.model // ""' <<<"$info")"
types="$(jq -r '[.events[].event_type] | group_by(.) | map("\(.[0])=\(length)") | join(" ")' <<<"$info")"
structured="$(jq '[.events[] | select(.event_type | IN("prompt","response","tool_call","file_write"))] | length' <<<"$info")"

pass() { if [ "$1" = 1 ]; then echo "✅"; else echo "❌"; fi; }
# 1: the title came from the prompt, not the directory-name default.
s1=$(pass "$([ -n "$title" ] && [ "$title" != "$(basename "$REPO")" ] && echo 1 || echo 0)")
s2=$(pass "$([ "$structured" -gt 0 ] && echo 1 || echo 0)")
s8=$(pass "$([ "$status" != live ] && echo 1 || echo 0)")

echo
echo "| agent | version | date | 1 title | 2 structured | 8 ended | model | events | session |"
echo "|---|---|---|---|---|---|---|---|---|"
echo "| $agent | ${version:-?} | $(date +%F) | $s1 \`${title:0:40}\` | $s2 | $s8 ($status) | ${model:-—} | $types (${took}s) | ${id:0:8} |"
