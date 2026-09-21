#!/bin/sh
# acceptance-manifest.sh — capture a preflight manifest for a live acceptance
# run of docs/plans/2026-09-12-reliability-acceptance.md, amended by Task 15 of
# docs/plans/2026-09-17-chief-engine-and-capacity-plan.md.
#
# WHAT THIS DOES: records git revision/dirty state, persona hashes, a redacted
# resolved-config hash, the run anchor this manifest would sit under, indices,
# provider caps, role byte budgets, retry/round/document limits, the operator
# deadlines from the acceptance runbook, and the worst-case per-company call
# ceiling 2*(rounds+3) from docs/plans/2026-09-12-reliability-acceptance.md.
#
# WHAT THIS DOES NOT DO: it never invokes `cfr`, never opens a network
# connection, never starts a subprocess that could spend a model call, and
# never reads a provider key into anything that reaches its own output. The
# project's own ./cfr.toml holds live DeepSeek, Alpaca, FRED and Alpha Vantage
# credentials, and no CFR_* variable can clear a key already set there — this
# script's only job with those values is to prove none of them appear below.
#
# USAGE:
#   sh docs/plans/acceptance-manifest.sh [indices]
# Run from the repository root, exactly like `cfr` itself resolves ./cfr.toml.
# [indices] is optional and mirrors the --indices flag an operator intends to
# pass to the acceptance run (comma-separated index keys); omit it to record
# "all" (cfr's own meaning of an empty --indices).
#
# POSIX sh only (tested under dash and bash) — no bashisms, so this runs
# identically as `sh docs/plans/acceptance-manifest.sh` or `./...`.
#
# Exit status: 0 and a JSON manifest on stdout, or non-zero on stderr with
# nothing printed to stdout — including, and especially, when the self-check
# below finds configured key material in what would otherwise be printed.

set -eu

cd "$(dirname "$0")/../.." # repo root, relative to this file's fixed location

TMPDIR_MANIFEST="$(mktemp -d)"
# Configured secrets are briefly staged in $TMPDIR_MANIFEST/secrets (see the
# self-check below) so cleanup must fire on interruption, not only on a normal
# exit: POSIX sh's EXIT trap alone does not fire on SIGINT/SIGTERM/SIGHUP, so a
# Ctrl-C mid-run could otherwise leave live keys on disk in this mktemp -d
# (mode 0700, but still a leftover) until the OS reaps /tmp. A bare `trap CMD
# INT` is not enough either: with a trap installed, the shell no longer
# terminates on the signal by default, so the script would resume running
# after a Ctrl-C unless the handler exits explicitly — the exit codes below
# are the conventional 128+signal.
cleanup_manifest() { rm -rf "$TMPDIR_MANIFEST"; }
trap cleanup_manifest EXIT
trap 'cleanup_manifest; exit 130' INT
trap 'cleanup_manifest; exit 143' TERM
trap 'cleanup_manifest; exit 129' HUP

# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------

sha256_hex() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

sha256_of_string() {
	if command -v sha256sum >/dev/null 2>&1; then
		printf '%s' "$1" | sha256sum | awk '{print $1}'
	else
		printf '%s' "$1" | shasum -a 256 | awk '{print $1}'
	fi
}

json_escape() {
	# Minimal escaping sufficient for the values this script ever produces
	# (paths, hex hashes, small integers, index keys, provider hostnames) —
	# not a general JSON encoder.
	printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

# resolve prints the last non-empty argument — used to apply the fixed
# precedence defaults -> user config -> project config -> env, left to right,
# exactly mirroring internal/config.Load's own order (config.go:301-312).
resolve() {
	result=""
	for v in "$@"; do
		if [ -n "$v" ]; then
			result="$v"
		fi
	done
	printf '%s' "$result"
}

# env_or prints the current value of the environment variable named by $1, or
# "" if unset — POSIX sh has no ${!name} indirection, so this uses eval. Never
# used on anything but a fixed, hard-coded variable name from this script's
# own source, never on attacker- or config-controlled data.
env_or() {
	eval "printf '%s' \"\${$1:-}\""
}

# toml_value reads one key from one section ("" for the document root) of a
# TOML file, ignoring commented-out (#-prefixed) lines, exactly the shape
# cfr.toml.example and this project's own ./cfr.toml use: flat "key = value"
# lines under "[section]" headers, no arrays of tables. It is deliberately
# narrow — a fixed allowlist of keys this script asks for — not a general
# TOML parser.
toml_value() {
	file="$1"
	section="$2"
	key="$3"
	[ -f "$file" ] || return 0
	awk -v want="$section" -v key="$key" '
		BEGIN { insect = (want == "") }
		/^[[:space:]]*#/ { next }
		/^[[:space:]]*\[/ {
			line = $0
			gsub(/^[[:space:]]*\[/, "", line)
			gsub(/\].*$/, "", line)
			insect = (line == want)
			next
		}
		{
			line = $0
			sub(/^[[:space:]]+/, "", line)
			if (line ~ "^" key "[[:space:]]*=") {
				if (!insect) next
				val = line
				sub("^" key "[[:space:]]*=[[:space:]]*", "", val)
				sub(/[[:space:]]*(#.*)?[[:space:]]*$/, "", val)
				gsub(/^"|"$/, "", val)
				print val
				exit
			}
		}
	' "$file"
}

USER_CONFIG="${CFR_CONFIG_HOME:-$HOME/.config/cfr/config.toml}"
PROJECT_CONFIG="./cfr.toml"

# ---------------------------------------------------------------------------
# 1. Git revision and dirty state
# ---------------------------------------------------------------------------

GIT_REVISION="$(git rev-parse HEAD)"
git status --porcelain=v1 >"$TMPDIR_MANIFEST/git-status" 2>/dev/null || true
GIT_DIRTY="false"
DIRTY_JSON="[]"
if [ -s "$TMPDIR_MANIFEST/git-status" ]; then
	GIT_DIRTY="true"
	: >"$TMPDIR_MANIFEST/dirty-items"
	while IFS= read -r line; do
		[ -z "$line" ] && continue
		path="${line#???}" # drop the 3-char porcelain status prefix
		case "$path" in
		*" -> "*) path="${path##*-> }" ;; # renames: hash the destination
		esac
		if [ -f "$path" ]; then
			h="$(sha256_hex "$path")"
		else
			h="absent (deleted or unreadable)"
		fi
		printf '{"path":"%s","sha256":"%s"}\n' "$(json_escape "$path")" "$(json_escape "$h")" \
			>>"$TMPDIR_MANIFEST/dirty-items"
	done <"$TMPDIR_MANIFEST/git-status"
	DIRTY_JSON="[$(tr '\n' ',' <"$TMPDIR_MANIFEST/dirty-items" | sed 's/,$//')]"
fi

# ---------------------------------------------------------------------------
# 2. Persona hashes (agents/*.md — runtime data, per CLAUDE.md)
# ---------------------------------------------------------------------------

: >"$TMPDIR_MANIFEST/persona-items"
for f in agents/*.md; do
	[ -f "$f" ] || continue
	h="$(sha256_hex "$f")"
	printf '{"path":"%s","sha256":"%s"}\n' "$(json_escape "$f")" "$(json_escape "$h")" \
		>>"$TMPDIR_MANIFEST/persona-items"
done
PERSONA_JSON="[$(tr '\n' ',' <"$TMPDIR_MANIFEST/persona-items" | sed 's/,$//')]"

# ---------------------------------------------------------------------------
# 3. Resolved configuration (non-secret fields only — an allowlist, not a
#    redact-after-the-fact blocklist: fields this script never queries cannot
#    leak through it regardless of what they contain)
# ---------------------------------------------------------------------------

# --- chief_engine (config.go:346-349 defaults an omitted key to "claude") ---
CHIEF_ENGINE="$(resolve "claude" \
	"$(toml_value "$USER_CONFIG" "" chief_engine)" \
	"$(toml_value "$PROJECT_CONFIG" "" chief_engine)" \
	"$(env_or CFR_CHIEF_ENGINE)")"

# --- research_mode (config.go:313-314 defaults an omitted key to "legacy") --
RESEARCH_MODE="$(resolve "legacy" \
	"$(toml_value "$USER_CONFIG" "" research_mode)" \
	"$(toml_value "$PROJECT_CONFIG" "" research_mode)" \
	"$(env_or CFR_RESEARCH_MODE)")"

# --- funnel geometry: rounds/documents/candidates/shortlist
#     (defaults from model.ResearchConfig.Defaults, internal/model/research.go:16-30) ---
ROUNDS="$(resolve "3" \
	"$(toml_value "$USER_CONFIG" research rounds)" \
	"$(toml_value "$PROJECT_CONFIG" research rounds)" \
	"$(env_or CFR_RESEARCH_ROUNDS)")"
DOCUMENTS="$(resolve "8" \
	"$(toml_value "$USER_CONFIG" research documents)" \
	"$(toml_value "$PROJECT_CONFIG" research documents)" \
	"$(env_or CFR_RESEARCH_DOCUMENTS)")"
CANDIDATES="$(resolve "24" \
	"$(toml_value "$USER_CONFIG" research candidates)" \
	"$(toml_value "$PROJECT_CONFIG" research candidates)" \
	"$(env_or CFR_RESEARCH_CANDIDATES)")"
SHORTLIST="$(resolve "12" \
	"$(toml_value "$USER_CONFIG" research shortlist)" \
	"$(toml_value "$PROJECT_CONFIG" research shortlist)" \
	"$(env_or CFR_RESEARCH_SHORTLIST)")"

# --- retry limits (defaults: orchestrator.go:366 Retry.MaxAttempts=2,
#     orchestrator.go:375 SynthesisMaxAttempts=1; no env override exists for
#     synthesis_max_attempts today, only the [retry] file key) ---
RETRY_MAX_ATTEMPTS="$(resolve "2" \
	"$(toml_value "$USER_CONFIG" retry max_attempts)" \
	"$(toml_value "$PROJECT_CONFIG" retry max_attempts)" \
	"$(env_or CFR_RETRY_MAX_ATTEMPTS)")"
SYNTHESIS_MAX_ATTEMPTS="$(resolve "1" \
	"$(toml_value "$USER_CONFIG" retry synthesis_max_attempts)" \
	"$(toml_value "$PROJECT_CONFIG" retry synthesis_max_attempts)")"

# --- role byte budgets (defaults: model.ResearchBudgets.Defaults,
#     internal/model/research_reliability.go:120-133) ---
role_budget() {
	# section default_input default_output env_in env_out
	section="$1"
	din="$2"
	dout="$3"
	envin="$4"
	envout="$5"
	in="$(resolve "$din" \
		"$(toml_value "$USER_CONFIG" "$section" input_bytes)" \
		"$(toml_value "$PROJECT_CONFIG" "$section" input_bytes)" \
		"$(env_or "$envin")")"
	out="$(resolve "$dout" \
		"$(toml_value "$USER_CONFIG" "$section" response_bytes)" \
		"$(toml_value "$PROJECT_CONFIG" "$section" response_bytes)" \
		"$(env_or "$envout")")"
	printf '{"input_bytes":%s,"response_bytes":%s}' "$in" "$out"
}
BUDGET_TRIAGE="$(role_budget research.budgets.triage 98304 12288 CFR_RESEARCH_TRIAGE_INPUT_BYTES CFR_RESEARCH_TRIAGE_RESPONSE_BYTES)"
BUDGET_RESEARCHER="$(role_budget research.budgets.researcher 98304 20480 CFR_RESEARCH_RESEARCHER_INPUT_BYTES CFR_RESEARCH_RESEARCHER_RESPONSE_BYTES)"
BUDGET_CHALLENGER="$(role_budget research.budgets.challenger 98304 12288 CFR_RESEARCH_CHALLENGER_INPUT_BYTES CFR_RESEARCH_CHALLENGER_RESPONSE_BYTES)"
BUDGET_CHIEF="$(role_budget research.budgets.chief 196608 24576 CFR_RESEARCH_CHIEF_INPUT_BYTES CFR_RESEARCH_CHIEF_RESPONSE_BYTES)"

# --- provider caps: base_url/model/max_tokens only, NEVER api_key. These
#     three fields are never queried by this script for [api]/[local]/
#     [chief_api]/[chief_fallback], so there is nothing here to redact after
#     the fact — the api_key key name simply never appears on the left of a
#     toml_value/env lookup below. ---
provider_block() {
	# section default_base default_model default_maxtok env_maxtok
	section="$1"
	dbase="$2"
	dmodel="$3"
	dmax="$4"
	envmax="$5"
	base="$(resolve "$dbase" \
		"$(toml_value "$USER_CONFIG" "$section" base_url)" \
		"$(toml_value "$PROJECT_CONFIG" "$section" base_url)")"
	model="$(resolve "$dmodel" \
		"$(toml_value "$USER_CONFIG" "$section" model)" \
		"$(toml_value "$PROJECT_CONFIG" "$section" model)")"
	maxtok="$(resolve "$dmax" \
		"$(toml_value "$USER_CONFIG" "$section" max_tokens)" \
		"$(toml_value "$PROJECT_CONFIG" "$section" max_tokens)" \
		"$(env_or "$envmax")")"
	printf '{"base_url":"%s","model":"%s","max_tokens":%s}' \
		"$(json_escape "$base")" "$(json_escape "$model")" "$maxtok"
}
# Unset base_url/model print as "" and an unset max_tokens falls back to
# apiengine.go's defaultMaxTokens (8192) at call time for api/local/chief_api
# (internal/orchestrator/chief.go:72-83) — this script reports that fallback
# explicitly rather than a bare 0, since 0 would misstate the request that
# would actually be sent. chief_fallback's own default is 32768, not 8192
# (orchestrator.go:270-272), because it is a reasoning-tier model.
CAP_API="$(provider_block api "" "" 8192 CFR_API_MAX_TOKENS)"
CAP_LOCAL="$(provider_block local "" "" 8192 CFR_LOCAL_MAX_TOKENS)"
CAP_CHIEF_API="$(provider_block chief_api "" "" 8192 CFR_CHIEF_API_MAX_TOKENS)"
CAP_CHIEF_FALLBACK="$(provider_block chief_fallback "https://api.deepseek.com" deepseek-reasoner 32768 CFR_CHIEF_FALLBACK_MAX_TOKENS)"

# --- indices: an explicit script argument (mirroring the operator's intended
#     --indices flag, since indices has no CFR_* env override — config.go has
#     none) beats the file's own `indices = [...]` array, which beats "all"
#     (cfr's own meaning of an empty selection). ---
INDICES_ARG="${1:-}"
indices_from_toml() {
	raw="$(toml_value "$1" "" indices)"
	[ -z "$raw" ] && return 0
	printf '%s' "$raw" | tr -d '[]"' | tr ',' '\n' | sed 's/^ *//; s/ *$//' | tr '\n' ',' | sed 's/,$//'
}
INDICES="$(resolve "all" \
	"$(indices_from_toml "$USER_CONFIG")" \
	"$(indices_from_toml "$PROJECT_CONFIG")" \
	"$INDICES_ARG")"

# --- run anchor: the runs/<ts> directory name this manifest would sit
#     beside if the acceptance run started now (internal/store/store.go:23-24
#     format, UTC) — this script never creates that directory itself. ---
RUN_ANCHOR="$(date -u +%Y-%m-%dT%H-%M-%S)"

# --- operator deadlines: fixed by the acceptance runbook
#     (docs/plans/2026-09-12-reliability-acceptance.md:64,72), not by any
#     resolved config field — overridable for a future revision of that
#     runbook without editing this script. ---
DEADLINE_SINGLE="$(env_or CFR_ACCEPTANCE_SINGLE_DEADLINE)"
[ -z "$DEADLINE_SINGLE" ] && DEADLINE_SINGLE="10m"
DEADLINE_FULL="$(env_or CFR_ACCEPTANCE_FULL_DEADLINE)"
[ -z "$DEADLINE_FULL" ] && DEADLINE_FULL="20m"

# --- worst-case per-company call ceiling, from the same runbook: "A company
#     has at most 2 * (rounds + 3) logical research/review calls including
#     format repairs: research rounds, initial challenge, revision and final
#     challenge." (2026-09-12-reliability-acceptance.md:23-24) ---
CEILING=$((2 * (ROUNDS + 3)))

# --- redacted resolved-config hash: a fingerprint of the non-secret fields
#     above (chief_engine, research_mode, indices, provider base_url/model/
#     max_tokens, role byte budgets, retry/round/document limits) so two
#     manifests can be compared for an identical *shape* of run without
#     comparing raw TOML — "redacted" because it is built exclusively from
#     the allowlisted fields already resolved above, never from a dump of the
#     config file itself, so a secret this script never reads cannot affect
#     the hash either. Excludes captured_at/run_anchor/git state, which are
#     expected to differ on every invocation regardless of configuration. ---
CONFIG_FINGERPRINT="chief_engine=$CHIEF_ENGINE|research_mode=$RESEARCH_MODE|indices=$INDICES|api=$CAP_API|local=$CAP_LOCAL|chief_api=$CAP_CHIEF_API|chief_fallback=$CAP_CHIEF_FALLBACK|triage=$BUDGET_TRIAGE|researcher=$BUDGET_RESEARCHER|challenger=$BUDGET_CHALLENGER|chief=$BUDGET_CHIEF|retry_max_attempts=$RETRY_MAX_ATTEMPTS|synthesis_max_attempts=$SYNTHESIS_MAX_ATTEMPTS|rounds=$ROUNDS|documents=$DOCUMENTS|candidates=$CANDIDATES|shortlist=$SHORTLIST"
REDACTED_CONFIG_HASH="$(sha256_of_string "$CONFIG_FINGERPRINT")"

# ---------------------------------------------------------------------------
# 4. Assemble the manifest text (still nothing secret has been read)
# ---------------------------------------------------------------------------

cat >"$TMPDIR_MANIFEST/manifest.json" <<EOF
{
  "captured_at": "$(json_escape "$RUN_ANCHOR")Z",
  "git": {
    "revision": "$(json_escape "$GIT_REVISION")",
    "dirty": $GIT_DIRTY,
    "dirty_files": $DIRTY_JSON
  },
  "persona_hashes": $PERSONA_JSON,
  "run_anchor": "$(json_escape "$RUN_ANCHOR")",
  "redacted_config_hash": "$(json_escape "$REDACTED_CONFIG_HASH")",
  "research_mode": "$(json_escape "$RESEARCH_MODE")",
  "chief_engine": "$(json_escape "$CHIEF_ENGINE")",
  "indices": "$(json_escape "$INDICES")",
  "provider_caps": {
    "api": $CAP_API,
    "local": $CAP_LOCAL,
    "chief_api": $CAP_CHIEF_API,
    "chief_fallback": $CAP_CHIEF_FALLBACK
  },
  "role_byte_budgets": {
    "triage": $BUDGET_TRIAGE,
    "researcher": $BUDGET_RESEARCHER,
    "challenger": $BUDGET_CHALLENGER,
    "chief": $BUDGET_CHIEF
  },
  "retry_round_document_limits": {
    "retry_max_attempts": $RETRY_MAX_ATTEMPTS,
    "synthesis_max_attempts": $SYNTHESIS_MAX_ATTEMPTS,
    "rounds": $ROUNDS,
    "documents": $DOCUMENTS,
    "candidates": $CANDIDATES,
    "shortlist": $SHORTLIST
  },
  "operator_deadlines": {
    "single_stock_case": "$(json_escape "$DEADLINE_SINGLE")",
    "full_run": "$(json_escape "$DEADLINE_FULL")",
    "source": "docs/plans/2026-09-12-reliability-acceptance.md"
  },
  "worst_case_call_ceiling_per_company": $CEILING,
  "worst_case_call_ceiling_formula": "2 * (rounds + 3), per docs/plans/2026-09-12-reliability-acceptance.md"
}
EOF

# ---------------------------------------------------------------------------
# 5. Self-check: prove no key material reached the manifest above, by
#    gathering every actually-configured secret (from file and from the same
#    env vars internal/config/config.go reads) and grepping the assembled
#    text for each one — full value and, per internal/redact.go's own
#    minSecret=8 threshold, its first 8 characters, since a truncated
#    fragment of a live key is still key material. This step reads secrets
#    into memory ONLY to check for their absence; none is ever assigned to a
#    variable that is printed, logged, or written to disk outside this
#    process's own memory and $TMPDIR_MANIFEST, which is removed on exit
#    (trap above) whether this script succeeds or fails.
# ---------------------------------------------------------------------------

collect_secret() {
	# section key env_var...  -> prints the first non-empty source found
	section="$1"
	key="$2"
	shift 2
	v="$(toml_value "$USER_CONFIG" "$section" "$key")"
	if [ -z "$v" ]; then
		v="$(toml_value "$PROJECT_CONFIG" "$section" "$key")"
	fi
	for envname in "$@"; do
		if [ -z "$v" ]; then
			v="$(env_or "$envname")"
		fi
	done
	printf '%s' "$v"
}

: >"$TMPDIR_MANIFEST/secrets"
collect_secret providers alphavantage_key ALPHAVANTAGE_API_KEY >>"$TMPDIR_MANIFEST/secrets"
printf '\n' >>"$TMPDIR_MANIFEST/secrets"
collect_secret providers fred_key FRED_API_KEY >>"$TMPDIR_MANIFEST/secrets"
printf '\n' >>"$TMPDIR_MANIFEST/secrets"
collect_secret providers alpaca_key_id APCA_API_KEY_ID CFR_ALPACA_KEY_ID >>"$TMPDIR_MANIFEST/secrets"
printf '\n' >>"$TMPDIR_MANIFEST/secrets"
collect_secret providers alpaca_secret_key CFR_ALPACA_SECRET_KEY >>"$TMPDIR_MANIFEST/secrets"
printf '\n' >>"$TMPDIR_MANIFEST/secrets"
collect_secret api api_key DEEPSEEK_API_KEY CFR_API_KEY >>"$TMPDIR_MANIFEST/secrets"
printf '\n' >>"$TMPDIR_MANIFEST/secrets"
collect_secret local api_key CFR_LOCAL_KEY >>"$TMPDIR_MANIFEST/secrets"
printf '\n' >>"$TMPDIR_MANIFEST/secrets"
collect_secret chief_api api_key CFR_CHIEF_API_KEY >>"$TMPDIR_MANIFEST/secrets"
printf '\n' >>"$TMPDIR_MANIFEST/secrets"
collect_secret chief_fallback api_key CFR_CHIEF_FALLBACK_API_KEY >>"$TMPDIR_MANIFEST/secrets"
printf '\n' >>"$TMPDIR_MANIFEST/secrets"

LEAK=0
while IFS= read -r secret; do
	[ -z "$secret" ] && continue
	prefix="$(printf '%.8s' "$secret")"
	if grep -qF -- "$secret" "$TMPDIR_MANIFEST/manifest.json" || grep -qF -- "$prefix" "$TMPDIR_MANIFEST/manifest.json"; then
		LEAK=1
	fi
done <"$TMPDIR_MANIFEST/secrets"

if [ "$LEAK" -ne 0 ]; then
	echo "acceptance-manifest.sh: refusing to print the manifest — configured key material was found in it. This is a bug in this script, not in the run." >&2
	exit 1
fi

cat "$TMPDIR_MANIFEST/manifest.json"
