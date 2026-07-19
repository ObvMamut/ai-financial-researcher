# Workflow: Single Stock

When the user types a ticker on the home screen, run the same deep-analysis agents on that
one name and return a single verdict. **Screening is skipped.**

## Differences from independent research

| Step          | Independent research        | Single stock                          |
|---------------|-----------------------------|---------------------------------------|
| Scouts        | 4 parallel, build shortlist | **skipped**                           |
| Shortlist     | merged ~8–12 names          | `[user ticker]`                       |
| Specialists   | 5, cover whole shortlist    | 5, cover the one ticker (richer depth)|
| Chief Analyst | ranks, returns **5** ideas  | returns **1** idea (`topN = 1`)       |

## Flow

```
user ticker → 5 Specialists (parallel) → Chief Analyst (Claude) → 1 idea
```

## Implementation note

Reuse the same code paths as independent research. The orchestrator takes a
`Mode` (`independent` | `single`), a `ticker` (single mode only), and `topN`. In single
mode it pre-populates the shortlist with the validated ticker and runs stages 2–3 only.

## Ticker validation

Before running, normalize and validate the ticker against the known universe
(`internal/universe`). If it isn't in any tracked index, still allow it but flag it as
"off-universe" so the Macro/Fundamentals specialists know to resolve the correct exchange
and symbology. Reject obviously malformed input in the TUI.

## Output

Same schema as the final ideas (`output-schema.md`): direction, confidence (0–100), quick
why — rendered as a single-idea results view. The full per-domain reports are still saved
under `runs/<ts>/`.
