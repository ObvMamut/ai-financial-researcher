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
  if has "**topN:** 1"; then
    cat <<'EOF'
Single-stock synthesis. Confluence Math: fundamentals 8 bull, technicals 6 bull, news 5 bull.

```json
{
  "mode": "single",
  "generated_at": "2026-07-18T00:00:00Z",
  "ideas": [
    {"rank": 1, "ticker": "AAPL", "name": "Apple Inc.", "index": "sp500",
     "direction": "BUY", "confidence": 71,
     "entry": 210.0, "stop": 199.5, "target": 231.0,
     "risk_reward": 2.0, "timeframe_days": 15,
     "position_note": "Half size into July 30 earnings",
     "why": "Fundamentals and technicals align bullish; sentiment neutral."}
  ],
  "notes": "Single-stock verdict."
}
```
EOF
    exit 0
  fi
  cat <<'EOF'
Synthesis reasoning. Deduped ASML/ASML.AS. Confluence Math per idea follows.

```json
{
  "mode": "independent",
  "generated_at": "2026-07-18T00:00:00Z",
  "ideas": [
    {"rank": 1, "ticker": "NVDA", "name": "NVIDIA Corporation", "index": "sp500",
     "direction": "BUY", "confidence": 82,
     "entry": 176.0, "stop": 165.5, "target": 198.0,
     "risk_reward": 2.1, "timeframe_days": 15,
     "position_note": "Full size, exit before Aug earnings",
     "why": "Four domains bullish; pullback entry near verified support."},
    {"rank": 2, "ticker": "ASML", "name": "ASML Holding NV", "index": "nq100",
     "direction": "BUY", "confidence": 78,
     "entry": 890.0, "stop": 845.0, "target": 985.0,
     "risk_reward": 2.1, "timeframe_days": 20,
     "position_note": "Full size",
     "why": "Bookings inflection with cheap-vs-growth multiple; base breakout."},
    {"rank": 3, "ticker": "JPM", "name": "JPMorgan Chase & Co.", "index": "sp500",
     "direction": "BUY", "confidence": 74,
     "entry": 305.0, "stop": 292.0, "target": 330.0,
     "risk_reward": 1.9, "timeframe_days": 15,
     "position_note": "Full size",
     "why": "Post-earnings drift with raised guidance; macro supportive."},
    {"rank": 4, "ticker": "7203.T", "name": "Toyota Motor Corporation", "index": "asia100",
     "direction": "BUY", "confidence": 70,
     "entry": 3400.0, "stop": 3230.0, "target": 3750.0,
     "risk_reward": 2.1, "timeframe_days": 20,
     "position_note": "Full size",
     "why": "Yen tailwind plus undervalued-growth bonus; sentiment uncrowded."},
    {"rank": 5, "ticker": "NKE", "name": "Nike Inc.", "index": "sp500",
     "direction": "SELL", "confidence": 66,
     "entry": 58.0, "stop": 62.4, "target": 49.0,
     "risk_reward": 2.0, "timeframe_days": 15,
     "position_note": "Half size",
     "why": "Bearish across news/fundamentals/technicals; guidance cut catalyst."}
  ],
  "notes": "ASML kept over ASML.AS duplicate. Diversified across four sectors."
}
```
EOF
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
  # $1 = domain name for the JSON tail
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
