package model

import "testing"

// The challenger must review every dossier claim exactly once (~444 bytes
// each measured live), so its response scales with claim count: a 12-claim
// review reached 13,750 bytes (runs/2026-09-23T15-50-35), and dossiers carry
// up to 17 claims. 12 KiB was tight by construction. The researcher's 32 KiB
// (from 20 KiB) is the other half of shrinking the dossier contract: 58% of
// researched names in the September 23 record produced no usable dossier,
// mostly through the byte budget, compaction or truncation. Triage and the
// Chief keep their original defaults.
func TestDefaultResponseBudgets(t *testing.T) {
	b := ResearchBudgets{}.Defaults()
	for role, want := range map[string]int{"triage": 12 << 10, "researcher": 32 << 10, "challenger": 16 << 10, "chief": 24 << 10} {
		if got := b.ForRole("thesis-" + role).ResponseBytes; got != want {
			t.Errorf("%s response budget = %d, want %d", role, got, want)
		}
	}
}
