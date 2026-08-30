#!/bin/sh
# Fake model CLI for hermetic tests and cheap manual TUI runs. Both testdata
# fake binaries (agy, claude) are copies of this script; it decides which agent
# it is playing by inspecting the persona header inside the -p prompt.
#
# Usage (matches internal/orchestrator/runner.go): fake -p "<prompt>" [--model <m>]
#
# CFR_FAKE_MODE targets specific failure paths:
#   badjson     - chief-analyst emits an invalid JSON tail (parse-error fallback)
#   chief-fail  - chief-analyst exits non-zero (synthesis-failed fallback)
#   scout-empty - scouts print nothing (retry, then empty-shortlist error path)
#   spec-fail   - specialists exit non-zero (missing-domain / <2-reports gate)
#   overclaim   - specialists score an off-shortlist ticker and claim missing:[]
#                 (coverage enforcement: scores stripped, CorrectedScores recorded)
#   no-tail     - specialists emit prose with no JSON tail (domain must fail)
#   off-base    - chief-analyst scores every idea 99, far outside the computed
#                 base ± band (confidence clamp + corrective re-prompt)
#   bad-levels  - chief-analyst places a 0.9σ stop at reward:risk 1.4, and does
#                 it again when asked to fix it (risk gate re-prompt, then drop)
prompt="$2"
mode="${CFR_FAKE_MODE:-ok}"

has() { case "$prompt" in *"$1"*) return 0 ;; *) return 1 ;; esac; }

# ── Scouts ──────────────────────────────────────────────────────────────────
if has "# Agent: Scout"; then
  [ "$mode" = "scout-empty" ] && exit 0
  # Echo a marker when the Stage 0.5 table actually reached the prompt, so the
  # hermetic run can assert the scouts screened data rather than a bare list.
  has "### Computed pre-screen" && echo "saw-prescreen-table"
  if has "**Index:** sp500"; then
    cat <<'EOF'
Screened the S&P 500 constituent list for 1-4 week swing setups.

```json
{"index": "sp500", "candidates": [
  {"ticker": "NVDA", "name": "NVIDIA Corporation", "bias": "bullish", "reason": "Pullback to prior breakout zone ahead of earnings"},
  {"ticker": "JPM", "name": "JPMorgan Chase & Co.", "bias": "bullish", "reason": "Post-earnings drift after Q2 beat"},
  {"ticker": "NKE", "name": "Nike Inc.", "bias": "bearish", "reason": "Guidance cut, inventory overhang"},
  {"ticker": "XOM", "name": "Exxon Mobil Corporation", "bias": "bullish", "reason": "Crude strength, base building"}
]}
```
EOF
  elif has "**Index:** nq100"; then
    cat <<'EOF'
Screened the Nasdaq 100 constituent list.

```json
{"index": "nq100", "candidates": [
  {"ticker": "ASML", "name": "ASML Holding NV", "bias": "bullish", "reason": "Bookings inflection, US ADR"},
  {"ticker": "AMD", "name": "Advanced Micro Devices", "bias": "bullish", "reason": "Datacenter share gains, flag pattern"},
  {"ticker": "TSLA", "name": "Tesla Inc.", "bias": "bearish", "reason": "Delivery miss risk into earnings"},
  {"ticker": "COST", "name": "Costco Wholesale", "bias": "neutral", "reason": "Comps steady, extended valuation"}
]}
```
EOF
  elif has "**Index:** eu50"; then
    cat <<'EOF'
Screened the EURO STOXX 50 constituent list.

```json
{"index": "eu50", "candidates": [
  {"ticker": "ASML.AS", "name": "ASML Holding NV", "bias": "bullish", "reason": "Bookings inflection, Amsterdam listing"},
  {"ticker": "SAP.DE", "name": "SAP SE", "bias": "bullish", "reason": "Cloud backlog acceleration"},
  {"ticker": "MC.PA", "name": "LVMH", "bias": "bearish", "reason": "China luxury demand soft"},
  {"ticker": "SAN.PA", "name": "Sanofi", "bias": "neutral", "reason": "Pipeline readout in window"}
]}
```
EOF
  else
    cat <<'EOF'
Screened the Asia 100 constituent list.

```json
{"index": "asia100", "candidates": [
  {"ticker": "7203.T", "name": "Toyota Motor Corporation", "bias": "bullish", "reason": "Weak yen tailwind, hybrid demand"},
  {"ticker": "6758.T", "name": "Sony Group", "bias": "bullish", "reason": "Gaming cycle, buyback"},
  {"ticker": "2330.TW", "name": "TSMC", "bias": "bullish", "reason": "AI capacity sold out"},
  {"ticker": "9988.HK", "name": "Alibaba Group", "bias": "bearish", "reason": "Regulatory overhang returning"}
]}
```
EOF
  fi
  exit 0
fi

# ── Chief Analyst ───────────────────────────────────────────────────────────
#
# Levels are derived from the verified quant block in the prompt rather than
# hard-coded. The risk gate checks stops and targets against each name's own
# realized volatility, so fixed numbers against a synthetic price series are
# rejected — correctly, but they would test nothing. Deriving them means the
# hermetic run exercises the gate on coherent ideas.
emit_ideas() {
  # $1 = mode string, $2 = how many ideas, $3 = confidence,
  # $4/$5 = stop and target distance in sigma (default 1.5 / 3.0)
  ss="${4:-1.5}"; ts="${5:-3.0}"
  printf '%s\n' "$prompt" | awk -v mode="$1" -v topn="$2" -v conf="$3" -v ss="$ss" -v ts="$ts" '
    $1=="-" && $3=="close" {
      t=$2; sub(/:$/,"",t); c=$4+0; sd=0
      for (i=1;i<=NF;i++) if ($i=="\xcf\x83_daily") { v=$(i+1); gsub(/[%,]/,"",v); sd=v+0 }
      if (c<=0 || sd<=0 || (t in seen)) next
      seen[t]=1; n++; tick[n]=t; cl[t]=c; sig[t]=sd
    }
    END {
      # NVDA first when present: the hermetic assertions name it as rank 1.
      k=0
      if ("NVDA" in cl) { order[++k]="NVDA" }
      for (i=1;i<=n && k<topn;i++) if (tick[i]!="NVDA") order[++k]=tick[i]
      printf "Synthesis reasoning. Confluence Math per idea: base +/- adjustments.\n\n```json\n"
      printf "{\n  \"mode\": \"%s\",\n  \"generated_at\": \"2026-07-18T00:00:00Z\",\n  \"ideas\": [\n", mode
      for (i=1;i<=k;i++) {
        t=order[i]; c=cl[t]
        # 1 sigma over a 15-day hold, in price. The default 1.5/3.0 sits
        # inside the gate bands at reward:risk 2.0.
        u = sd_unit(sig[t], c)
        printf "    {\"rank\": %d, \"ticker\": \"%s\", \"direction\": \"BUY\", \"confidence\": %d,\n", i, t, conf
        printf "     \"entry\": %.2f, \"stop\": %.2f, \"target\": %.2f,\n", c, c-ss*u, c+ts*u
        printf "     \"risk_reward\": %.2f, \"timeframe_days\": 15,\n", ts/ss
        printf "     \"position_note\": \"sized by the app\",\n"
        printf "     \"why\": \"Fake synthesis for %s; levels derived from verified sigma.\"}%s\n", t, (i<k ? "," : "")
      }
      printf "  ],\n  \"notes\": \"Hermetic fake synthesis.\"\n}\n```\n"
    }
    function sd_unit(sd, c) { return (sd/100) * sqrt(15) * c }
  '
}

if has "# Agent: Chief Analyst"; then
  case "$mode" in
    chief-fail) echo "fake chief crashed" >&2; exit 1 ;;
    badjson)
      cat <<'EOF'
Synthesis reasoning here.

```json
{"mode": "independent", "ideas": [ this is not valid json !!! ]}
```
EOF
      exit 0 ;;
  esac
  if [ "$mode" = "off-base" ]; then
    # Confidence asserted rather than derived: the exact failure the base-score
    # anchor exists to catch.
    emit_ideas independent 5 99
    exit 0
  fi
  if [ "$mode" = "bad-levels" ]; then
    # A stop inside the noise band at a reward:risk the old prose merely
    # "preferred" against — and unchanged after the corrective re-prompt.
    emit_ideas independent 5 55 0.9 1.26
    exit 0
  fi
  if has "**topN:** 1"; then
    emit_ideas single 1 71
    exit 0
  fi
  emit_ideas independent 5 55
  exit 0
fi

# ── Specialists ─────────────────────────────────────────────────────────────
[ "$mode" = "spec-fail" ] && { echo "fake specialist crashed" >&2; exit 1; }
if [ "$mode" = "no-tail" ]; then
  # A refusal or a truncated response: prose, no structured tail. This used to
  # pass as "done" on non-empty stdout alone and flow into synthesis unnoticed.
  echo "I am unable to complete this analysis without additional data."
  exit 0
fi
if [ "$mode" = "overclaim" ]; then
  # The 2026-08-28 failure mode: score everything, including a ticker that was
  # never on the shortlist, and declare full coverage.
  cat <<EOF
Fake report asserting coverage it does not have.

\`\`\`json
{"domain": "$(
  case "$prompt" in
    *"# Agent: News Analyst"*)         echo news ;;
    *"# Agent: Fundamentals Analyst"*) echo fundamentals ;;
    *"# Agent: Quant Analyst"*)        echo quant ;;
    *"# Agent: Sentiment Analyst"*)    echo sentiment ;;
    *)                                 echo macro ;;
  esac
)", "scores": [
  {"ticker": "NVDA", "bias": "bullish", "strength": 8, "note": "strong"},
  {"ticker": "ZZZZ", "bias": "bullish", "strength": 9, "note": "never on the shortlist"}
], "missing": []}
\`\`\`
EOF
  exit 0
fi

spec_scores() {
  # $1 = domain name for the JSON tail. The markers let the hermetic run assert
  # that each role's prompt actually carried the computed block it is written
  # against, rather than only that the role ran.
  has "### Verified market regime" && echo "saw-regime-block"
  has "### Verified price context" && echo "saw-price-context"
  cat <<EOF
Fake $1 report covering the shortlist. Findings per ticker follow.

- NVDA: constructive [source:example.com 2026-07-18]
- NKE: deteriorating [source:example.com 2026-07-18]

\`\`\`json
{"domain": "$1", "scores": [
  {"ticker": "NVDA", "bias": "bullish", "strength": 8, "note": "strong"},
  {"ticker": "ASML", "bias": "bullish", "strength": 7, "note": "solid"},
  {"ticker": "JPM", "bias": "bullish", "strength": 6, "note": "steady"},
  {"ticker": "7203.T", "bias": "bullish", "strength": 6, "note": "tailwind"},
  {"ticker": "NKE", "bias": "bearish", "strength": 7, "note": "weak"},
  {"ticker": "TSLA", "bias": "bearish", "strength": 4, "note": "mixed"},
  {"ticker": "AAPL", "bias": "bullish", "strength": 6, "note": "steady"}
], "missing": []}
\`\`\`
EOF
}

if has "# Agent: News Analyst"; then spec_scores "news"; exit 0; fi
if has "# Agent: Fundamentals Analyst"; then spec_scores "fundamentals"; exit 0; fi
if has "# Agent: Technicals Analyst"; then spec_scores "technicals"; exit 0; fi
if has "# Agent: Quant Analyst"; then spec_scores "quant"; exit 0; fi
if has "# Agent: Sentiment Analyst"; then spec_scores "sentiment"; exit 0; fi
if has "# Agent: Macro Analyst"; then spec_scores "macro"; exit 0; fi

echo "fake-cli: unrecognised persona in prompt" >&2
exit 1
