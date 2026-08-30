# agents.v1 — frozen baseline personas

These are the agent personas exactly as they stood at commit `f137f2a`, immediately
before the research-quality overhaul rewrote them. They exist to be the control arm of
an A/B comparison, and for no other purpose.

**Do not edit anything in this directory.** A control that drifts is not a control. If a
persona here needs to change, the answer is that the experiment is over.

Run against them with:

```bash
CFR_AGENTS_DIR=agents.v1 go run ./cmd/cfr run --json
```

Every run records which set it used in `metadata.json` (`persona_set` and the per-role
`persona_sha`), and `cfr scoreboard` groups closed ideas by that key. See
`docs/workflow/scoreboard.md` for how to read the comparison and when it is safe to.

Note that these personas predate several in-process changes the code now makes
unconditionally — computed base scores, the risk gate, the verified earnings calendar.
The comparison is therefore between *prompt sets under today's pipeline*, not between
the old system and the new one. That is the question worth answering: the pipeline is
not going back.
