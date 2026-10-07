# Acceptance run and news audit (2026-10-07)

## Live acceptance run: `runs/2026-10-07T14-40-51` (14:40Z, inside the US session)

This run checks the four fixes merged that morning (`8e42983`, `e9f9276`, `6cd1079`,
`79e794e`).

| Check | Result |
|---|---|
| `event_in_window` only where earnings fall before the 15-session exit | ✓ FCX 2026-10-22 only. TTE.PA (10-29) and ILMN/MRK/INGA (10-29) are correctly unmarked: 10-29 is the session after the exit (10-28) |
| `repeat_of` on re-shipped calls | ✓ CRM, MU, ILMN → 10-06 run; FCX → 05:13Z run; NKE fresh |
| No exit instructions in `position_note` | ✓ All five are "watch…" notes. FCX's says the earnings is "a scheduled volatility event to monitor, not a thesis event" |
| Mapped foreign names covered by news | ✓ in the news pack (PHIA.AS, INGA.AS, ENI.MI, TTE.PA all `true`) |
| No options data errors in-session | ✗ at first sight: 2 open-interest-blind, 5 IV-withheld. **Explained, see below** |

**Options errors: a stale-cache artifact, not new failures.**
- Every errored chain carries exactly the numbers from the 05:13Z pre-open run. MU, for
  example: 678 strikes, 639 with volume.
- Each one is a name whose chain was cached at 05:14Z, before the pre-open cache guard
  existed.
- The seven names fetched fresh in-session (ENI, ILMN, OKTA, NKE, INGA, 8306, MRK) had
  **no** option errors.
- So pre-open fetching is the whole cause. The 2026-09-04 "in-session" failures most
  likely came from that morning's 06:11Z run in the same way.
- The guard (`offSession` in `pack.go`) prevents this from now on.

**New defect found and fixed: news report truncated** (`488b6f5`).
- The news specialist's answer ended mid-sentence ("…Insid") with `finish_reason: "stop"`.
- It had no JSON tail, and the domain failed on its only attempt.
- Every row lost its news score, and four names fell to the evidence floor as quant-only:
  ENI.MI, TTE.PA, INGA.AS, PHIA.AS.
- Fix: a specialist report with no tail is now asked for once more, with the same prompt.

## News coverage audit: all 115 foreign listings, live, no models

**Mapped to a NYSE/NASDAQ line (26 names).**
- Covered: 25 of 26 after this morning's fixes, and 26 of 26 later in the session. SAN.MC
  only has wraps on some fetches.

**Unmapped (89 names).** Coverage went from **0 of 89 to 40 of 89** (`e4c07d0`).
- Yahoo's search returns *nothing* for any foreign local symbol (SAP.DE, 7203.T,
  0700.HK, …), so every unmapped name had no news domain at all.
- Searching by company name returns fresh items tagged with the listing.
- An item now counts only if it is tagged *and* its headline names the company.

| Suffix | covered / names | Suffix | covered / names |
|---|---|---|---|
| .DE | 10/14 | .T | 9/12 |
| .PA | 8/9 | .HK | 5/9 |
| .AX | 6/8 | .SI | 3/5 |
| .MI | 3/3 | .MC | 2/3 |
| .AS | 2/2 | .HE | 2/3 |
| .KS | 1/7 | .TW | 1/5 |
| .NS | 0/6 | .BK | 0/2 |
| .B | 0/1 | | |

Korea, Taiwan, India and Thailand remain mostly dark; English-language coverage of
those issuers under their own names is thin. Known limit: a company whose name is its
ticker root cannot be told apart from a US ticker with the same letters. CSL.AX vs
Carlisle (CSL) is the case seen.

The evidence floor is unchanged throughout. More foreign names can now pass it only
because they really do have non-price evidence.
