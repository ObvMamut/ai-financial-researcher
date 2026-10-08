# Keep, cut or stop: a memo for the owner (2026-10-01, updated 2026-10-07)

**The question.** Eighteen pre-registered lab tests have now run: fifteen on today's constituents
and three on a survivorship-free US universe. None has found an edge that survives cost, on the
live 15-session hold or on 21- and 63-session holds. This memo sets out three options. The owner
decided on 2026-10-07; the decision is recorded at the end.

## What the evidence says

| Tested | Result | Source |
|---|---|---|
| The pre-screen composite, 15-session top-5, net of 30bp | positive in 5 of 11 years (E1 fired); β-adj. IC10 t 0.66 | `2026-09-25-lab-e1-e3.md` |
| The model stages (specialists, Chief) | no value above the funnel: domain ICs −0.08…+0.07; Chief picks +0.55% vs +0.58% for names left out | `2026-09-23-evidence/` |
| Post-earnings drift and the earnings premium (D1–D3) | t −0.67, −0.03, 1.84 | `2026-09-30-lab-drift.md` |
| The composite's top-5 at 21 / 63 sessions (H1) | +0.34% / +2.14% net per hold; t 1.08 / 2.31 | `2026-10-01-lab-horizon.md` |
| 12-1 momentum alone at 21 / 63 sessions (H2) | t 0.68 / 0.91, negative in the US | `2026-10-01-lab-horizon.md` |
| The screen on a survivorship-free US universe, 2017-09-29..2026-09-25 (PIT-IC10, PIT-H1-63, PIT-E1) | β-adj. IC10 t −0.30; top-5 at 63 sessions +2.30% net per hold, t 1.30 (same-window sample +3.56%, t 2.24); positive in 3 of 10 years | `2026-10-07-pit-lab.md` |
| The 15 sample tests, Holm-adjusted; the 3 point-in-time tests | smallest Holm p 0.159 (H1-63); PIT one-sided p 0.617 / 0.097 / 0.945. None below 0.05 | `2026-10-01-lab-horizon.md`; `2026-10-07-evidence/pit-9y.json` |

The closest result was H1-63, the screen's top names held for about three months. On the
survivorship-free universe the same statistic falls to t 1.30. Survivorship had inflated it by 1.26
points per hold, about a third. It is no longer a lead. Its out-of-sample test, OOS-H1-63, uses
weeks after 2026-09-25 and is registered and held out in code. It is the one open question, and it
cannot be evaluated before about 2027-12.

## What changed since 2026-10-01

- The point-in-time US lab (`cfr backtest --universe pit`).
- OOS-H1-63 registered (`09a35a6`) and held out in code (`b7709b8`).
- `cfr canary`: nine probes, exit 1 on a silent failure (`c634b0c`).
- Foreign news coverage from 25/115 to 68/114 (mapped 27/27, unmapped 41/87; Korea, Taiwan, India
  and Thailand still mostly dark).
- The options chain abstains before the US open rather than reporting blind values, and is not
  cached (`8e42983`).
- Live-system risk and data fixes since this memo, none of them a signal change: the merit_veto
  pair cap (`ce22704`), silent catastrophe-stop placement (`78e0b60`), earnings-in-hold stamping
  (`e9f9276`).
- E3's rule fires on both 9-year US replays, but it is not a registered decision there: E3 was
  decided on the 10-year run, where it did not fire.

## The options

**1. Keep CFR as a measurement and research tool (recommended).**
- *What runs:* the backtest lab, the scoreboard and the shadow arms. Live runs happen only when a
  registered question needs fresh data.
- *Cost:* 139–165K DeepSeek tokens per legacy live run (mean 152,715), measured from
  `metadata.json` usage over the four runs 2026-09-24..2026-10-07. That is 112–128K on
  deepseek-chat (scouts, specialists, post-mortem) and 27–41K on deepseek-v4-pro (Chief). It is
  165K when a run redraws the 24-hour post-mortem and about 140K when it reuses it. The lab is
  keyless and makes no model calls.
- *What it buys:* OOS-H1-63's single evaluation (~2027-12), the reusable lab, and one last
  registered round of genuinely new hypotheses.
- *Risk:* it turns into a hobby that spends attention without changing a decision. Guard against
  that by registering a question before every live run.

**2. Cut the live cadence to zero, keep the code.**
- *What runs:* nothing, until you choose to restart.
- *Cost:* none.
- *What it loses:* the live record stops growing. That record was never going to decide anything
  soon in any case: a 50bp edge over two weeks needs about 550 independent calls, and a run ships
  five. The last live run was 2026-10-07 (14:40Z).
- *Risk:* the data caches and keys age, so a restart will need some repair. `cfr canary` now
  measures that repair in one command.

**3. Stop.**
- *What runs:* nothing. Archive the repository.
- *Cost:* none from here on. The work already done is kept as a record.
- *What it loses:* OOS-H1-63's evaluation and the reusable lab.
- *When this is right:* if the aim was a tradeable edge on this horizon by now. On that aim, the
  evidence says CFR has not found one.

## Recommendation

**Option 1, narrowed to one more round of genuinely new hypotheses** (N1 MAX, N2 IVOL, N3 FIP), run
once on the point-in-time lab at a 21-session horizon. If that round fails too, move to option 2
and leave OOS-H1-63 as the only live question until 2027-12. A fourth round of screen tuning is not
on the table: eighteen null results say the price-only screen at these horizons is exhausted.

## Owner decision (2026-10-07)

Option 1, narrowed, as recommended. If N1–N3 all fail, option 2 follows with no further question.
Separately, nq100.csv's 11 non-members are replaced by current members.

No setting or default changed with this memo.

## Outcome (2026-10-08)

N1–N3 all fail on the point-in-time US lab at 21 sessions:
- N1 MAX: t 1.66.
- N2 IVOL: t 2.13.
- N3 FIP: t 0.45.

The smallest Holm p over the 21-test register is 0.333 (`2026-10-08-lab-anomalies.md`).

**Option 2 is in force from 2026-10-08.**
- Live runs are suspended by owner decision.
- `cfr canary`, `cfr backtest` and `cfr scoreboard` remain supported.
- OOS-H1-63 is the only open question. Its single evaluation is `cfr backtest --evaluate-oos`,
  once 52 held-out weeks have matured (about 2027-12).
