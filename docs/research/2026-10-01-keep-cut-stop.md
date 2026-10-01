# Keep, cut or stop: a memo for the owner (2026-10-01)

**The question.** Fifteen pre-registered lab tests have now run. None has found an edge that
survives cost, on the live 15-session hold or on 21- and 63-session holds. This memo sets out three
options. The decision is yours, and no code has been changed to anticipate it.

## What the evidence says

| Tested | Result | Source |
|---|---|---|
| The pre-screen composite, 15-session top-5, net of 30bp | positive in 5 of 11 years (E1 fired); β-adj. IC10 t 0.66 | `2026-09-25-lab-e1-e3.md` |
| The model stages (specialists, Chief) | no value above the funnel: domain ICs −0.08…+0.07; Chief picks +0.55% vs +0.58% for names left out | `2026-09-23-evidence/` |
| Post-earnings drift and the earnings premium (D1–D3) | t −0.67, −0.03, 1.84 | `2026-09-30-lab-drift.md` |
| The composite's top-5 at 21 / 63 sessions (H1) | +0.34% / +2.14% net per hold; t 1.08 / 2.31 | `2026-10-01-lab-horizon.md` |
| 12-1 momentum alone at 21 / 63 sessions (H2) | t 0.68 / 0.91, negative in the US | `2026-10-01-lab-horizon.md` |
| All 15 tests, Holm-adjusted | smallest p 0.159 (H1-63); none below 0.05 | `2026-10-01-lab-horizon.md` |

The closest result is H1-63: the screen's top names held for about three months. It is positive in
every half and every region, but it misses the bar. It is also the figure that survivorship
flatters most, because the universe holds only today's index members. It is a lead, not an edge.

## The options

**1. Keep CFR as a measurement and research tool (recommended).**
- *What runs:* the backtest lab, the scoreboard and the shadow arms. Live runs happen only when a
  registered question needs fresh data.
- *Cost:* close to nothing. The lab is keyless and makes no model calls. A legacy live run cost
  about 165K tokens in September.
- *What it buys:* the two things that could still confirm or kill H1-63 honestly, which are weeks
  the lab has not yet seen and, if one can be had, a survivorship-free universe (C5). It also
  keeps the infrastructure, which already works, for any new hypothesis worth registering.
- *Risk:* it turns into a hobby that spends attention without changing a decision. Guard against
  that by registering a question before every live run.

**2. Cut the live cadence to zero, keep the code.**
- *What runs:* nothing, until you choose to restart.
- *Cost:* none.
- *What it loses:* the live record stops growing. That record was never going to decide anything
  soon in any case: a 50bp edge over two weeks needs about 550 independent calls, and a run ships
  five. The last live run was 2026-09-24.
- *Risk:* the data caches and keys age, so a restart will need some repair.

**3. Stop.**
- *What runs:* nothing. Archive the repository.
- *Cost:* none from here on. The work already done is kept as a record.
- *What it loses:* the option value on H1-63 and the reusable lab.
- *When this is right:* if the aim was a tradeable edge on this horizon by now. On that aim, the
  evidence says CFR has not found one.

## Recommendation

**Option 1, with no recurring live runs.** In practice this is option 2 plus a working lab. Two
things are worth doing next, and both come before any new trading logic:

1. Re-test H1-63 out of sample, in about a year of new weeks, with the test registered now. Do not
   re-test it on the same ten years.
2. Look for a keyless, point-in-time index-membership source. That is the only way to remove the
   survivorship bias that inflates every positive number above.

Nothing in the live system has changed: ideas, selection, time exit and scoreboard horizon are as
they were. Whether CFR keeps proposing trades is your decision.
