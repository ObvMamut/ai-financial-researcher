package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

// Coverage caps. A name only one or two domains could look at cannot be a
// high-confidence idea however loud those domains are. This used to be prose in
// the persona ("max 65 if one domain is missing, 55 if two or more"), which the
// model applied by counting report sections rather than by weight — so a run
// that lost macro (10% of the weight) was capped exactly as hard as one that
// lost quant (35%). Weighted coverage is the honest measure and it is arithmetic.
//
// Since the score is divided by *total* weight (see computeBaseScores), coverage
// already bounds the score arithmetically. But it does not bound it as tightly as
// this comment used to claim: "a quant-only name cannot exceed 35 because quant
// is 35% of the weight" was true before the ReferenceTotal rescaling below and is
// not true after it — the ceiling is 0.35/0.77 ≈ 45, and on 2026-09-01 O39.SI's
// single `quant 8` scored 36 against ORCL's five-domain 26. So scarceCap = 40
// sits *above* what a scarce-coverage name reaches at the strengths the rubrics
// actually use, and never binds. What bounds a lone domain outranking a consensus
// is the funnel: universe.MeritCaps.ThinlyCovered stops the shortlist filling up
// with names most of the five domains cannot see. The caps stay as a redundant floor:
// they cost nothing and they keep working if the weights are reconfigured.
const (
	thinCoverage    = 0.6 // below this share of total domain weight → cap 55
	scarceCoverage  = 0.4 // below this → cap 40
	thinCap         = 55
	scarceCap       = 40
	degradedConfCap = 55 // the degraded path does no cross-domain reasoning at all
)

// The confidence a base score carries is expressed against
// model.ReferenceTotal — the strongest verdict the five rubrics jointly permit —
// rather than against an unreachable strength of 10 everywhere.
//
// `Σ w·sign·strength/10` over the raw total weight can only reach 100 if all
// five domains agree at strength 10, but every persona reserves 9–10 for "rare"
// and agents/macro.md caps itself outright ("**5** … **This is your ceiling**").
// The attainable maximum is ~0.77, so the scale never used its top quarter.
// Across the four runs from 2026-08-31 — the first with this arithmetic live —
// the highest base *anywhere* was 45.5 and the shipped ideas ran 26–45, while
// every consumer of the number still treated 50 as mediocre and 70 as good. MRK
// on 2026-09-01 was the strongest three-domain agreement this system can produce
// (quant +6, news +6, fundamentals +6) and scored 39.
//
// The table lives in internal/model because the scoreboard has to read a past
// idea's recorded domain scores on the same scale a live run produces them.

// BaseScore is the deterministic weighted read on one shortlisted ticker,
// computed in-process from the specialists' structured tails before the Chief
// Analyst sees them.
//
// It exists so that confidence is anchored to arithmetic the run can reproduce.
// Previously the Chief was handed five prose reports and a weights table and
// asked to do the weighting itself; nothing checked the result, and a
// confidence of 78 was an assertion rather than a computation.
type BaseScore struct {
	Ticker string `json:"ticker"`
	// Direction is the sign of the weighted vote; empty when no domain scored
	// the ticker, or when the signed domains cancel exactly.
	Direction model.Direction `json:"direction,omitempty"`
	// Signed is the weighted score over the *full* domain weight, ∈ [−1, 1],
	// positive = bullish. A domain with no data contributes 0 to it, which is
	// what makes thin coverage score below thick coverage. It is kept as the
	// auditable raw figure; Confidence is scored off Scaled.
	Signed float64 `json:"signed"`
	// Scaled is Signed re-expressed against referenceTotal — the strongest joint
	// verdict the five rubrics permit — rather than against an unreachable
	// strength of 10 everywhere. Same divisor for every name, so it changes the
	// scale and never the ordering.
	Scaled float64 `json:"scaled"`
	// Confidence is |Scaled|·100 with the coverage cap applied.
	Confidence int `json:"confidence"`
	// Cap is the coverage cap that was applied, or 0 if none bound.
	Cap int `json:"cap,omitempty"`
	// CoveredWeight is the share of total domain weight that actually scored
	// this ticker (a domain that listed it as `missing` does not count).
	CoveredWeight float64 `json:"covered_weight"`
	// Consensus is how much of the evidence's magnitude survives the domains
	// disagreeing: `|Σ w·sign·s| / Σ w·|s|`, ∈ [0, 1]. One means every domain
	// that spoke pointed the same way; zero means they cancelled exactly.
	//
	// It is computed over the *scoring* domains only, because a zero-weight
	// domain is skipped before it reaches either side of the ratio. That matters:
	// while macro was weighted it agreed with the nomination on every name in
	// every recent run, so this figure reported unanimity for names on which
	// nothing independent had spoken at all.
	//
	// It reads no evidence Signed does not already read, and it changes nothing —
	// ranking, capping and the Chief's band are all untouched. It exists because
	// the base score is one number doing two jobs, *how much evidence* and *how
	// much agreement*, and the two are not the same fact. On 2026-09-01 O39.SI
	// scored 32 on one loud domain and MRK scored 31 on five that disagreed; the
	// number said they were equally good ideas and every reader of it — the
	// Chief, the confidence bar, the scoreboard's buckets — had no way to see
	// which was which.
	Consensus float64 `json:"consensus"`
	// Domains maps each domain that scored the ticker to its signed strength
	// (−10…+10). A neutral bias is 0 but still counts as coverage.
	Domains map[string]int `json:"domains,omitempty"`
}

// For returns the base confidence for a proposed direction. A direction that
// opposes the domains' verdict starts from zero: the weighted vote reading
// bearish is not evidence for a BUY at any strength.
func (b BaseScore) For(dir model.Direction) int {
	if b.Direction == "" || dir != b.Direction {
		return 0
	}
	return b.Confidence
}

// domainWeightMap flattens the configured weights into the domain keys the
// specialists actually emit.
func domainWeightMap(w model.DomainWeights) map[string]float64 { return w.Map() }

// computeBaseScores turns the specialist reports into one weighted score per
// shortlisted ticker, ranked best-first. Every shortlisted name appears, even
// one no domain scored — an absent row would read as an oversight, whereas a
// row with zero coverage says plainly that there is nothing to reason from.
func computeBaseScores(w model.DomainWeights, reports []agents.ReportContext, shortlist []model.Candidate) []BaseScore {
	weights := domainWeightMap(w)
	totalWeight := 0.0
	for _, v := range weights {
		if v > 0 {
			totalWeight += v
		}
	}
	reference := model.ReferenceTotal(w)

	type accum struct {
		weighted float64
		// gross is Σ w·strength/10 with the signs stripped — the magnitude the
		// domains would have produced had they all agreed. It is the denominator
		// of Consensus.
		gross   float64
		covered float64
		domains map[string]int
	}
	byTicker := make(map[string]*accum)
	order := make([]string, 0, len(shortlist))
	for _, c := range shortlist {
		t := strings.ToUpper(strings.TrimSpace(c.Ticker))
		if t == "" || byTicker[t] != nil {
			continue
		}
		byTicker[t] = &accum{domains: map[string]int{}}
		order = append(order, t)
	}

	for _, rep := range reports {
		raw, ok := extractLastJSON(rep.Content)
		if !ok {
			continue
		}
		var sr model.SpecialistResult
		if err := json.Unmarshal([]byte(raw), &sr); err != nil {
			continue
		}
		domain := strings.ToLower(strings.TrimSpace(sr.Domain))
		if domain == "" {
			domain = strings.ToLower(rep.Domain)
		}
		weight, ok := weights[domain]
		if !ok || weight <= 0 {
			continue
		}
		for _, s := range sr.Scores {
			a := byTicker[strings.ToUpper(strings.TrimSpace(s.Ticker))]
			if a == nil {
				continue // never on the shortlist; the enforcement pass already logged it
			}
			if _, seen := a.domains[domain]; seen {
				continue // one score per domain per ticker; the first one stands
			}
			strength := s.Strength
			if strength < 0 {
				strength = 0
			} else if strength > 10 {
				strength = 10
			}
			sign := 0
			switch s.Bias {
			case model.BiasBullish:
				sign = 1
			case model.BiasBearish:
				sign = -1
			}
			a.weighted += weight * float64(sign) * float64(strength) / 10
			a.gross += weight * float64(strength) / 10
			a.covered += weight
			a.domains[domain] = sign * strength
		}
	}

	out := make([]BaseScore, 0, len(order))
	for _, t := range order {
		a := byTicker[t]
		b := BaseScore{Ticker: t, Domains: a.domains}
		if a.covered > 0 && totalWeight > 0 {
			// Divide by the *total* weight, not the covered weight: a domain with
			// no data for this name casts an explicit neutral vote.
			//
			// Renormalising over the covered weight instead is how the
			// 2026-09-01 run put three quant-only foreign listings above the one
			// name all five domains had read. Dividing by what was actually
			// present means a missing domain never shrinks the magnitude — a lone
			// loud domain renormalises to 50-60 and lands exactly on its coverage
			// cap, while five domains that partly disagree average down to 35. The
			// cap was acting as a floor-boost for thin evidence rather than as a
			// ceiling. Under this arithmetic the same quant-only names score 18-21
			// and AMGN's 35 leads, which is the ordering the evidence supports.
			b.Signed = a.weighted / totalWeight
			b.CoveredWeight = a.covered / totalWeight
			// Confidence is scored against what the domains can jointly express,
			// not against an unreachable 10-across-the-board. Same denominator for
			// every name, so this rescales without reordering. See
			// model.ReferenceStrength.
			if reference > 0 {
				b.Scaled = a.weighted / reference
				if b.Scaled > 1 {
					b.Scaled = 1
				} else if b.Scaled < -1 {
					b.Scaled = -1
				}
			}
		}
		if a.gross > 0 {
			b.Consensus = abs(a.weighted) / a.gross
		}
		if len(b.Domains) == 0 {
			b.Domains = nil
		}
		switch {
		case b.Signed > 0:
			b.Direction = model.DirectionBuy
		case b.Signed < 0:
			b.Direction = model.DirectionSell
		}
		conf := int(abs(b.Scaled)*100 + 0.5)
		switch {
		case b.CoveredWeight < scarceCoverage:
			b.Cap = scarceCap
		case b.CoveredWeight < thinCoverage:
			b.Cap = thinCap
		}
		if b.Cap > 0 && conf > b.Cap {
			conf = b.Cap
		}
		if a.covered == 0 {
			b.Cap = 0 // nothing to cap; the empty row speaks for itself
		}
		b.Confidence = conf
		out = append(out, b)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Ticker < out[j].Ticker
	})
	return out
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// baseScoreDomains is the fixed column order of the per-domain cells, heaviest
// weight first so the table reads in order of what actually moves the score.
// Macro is absent: it carries zero weight (model.DefaultDomainWeights) and a
// column of nothing but `·` would read as a domain that had failed rather than
// as one that does not vote.
var baseScoreDomains = []string{"quant", "news", "fundamentals", "sentiment"}

// baseScoreBlock renders the computed scores for the chief-analyst prompt. The
// band is stated in the header because the number the Chief may move by is part
// of the instruction, not a separate rule it has to remember.
func baseScoreBlock(bases []BaseScore, band int) string {
	if len(bases) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("### Computed base scores (authoritative)\n\n")
	sb.WriteString("Computed in-process from the `scores` tails of the reports below, using the weights above: ")
	sb.WriteString("`base = Σ wᵈ · signᵈ · strengthᵈ/10` over the *full* domain weight — a domain with no data for a name votes 0 — ")
	sb.WriteString("then expressed as a share of the strongest joint verdict the scoring rubrics allow (strength 8). ")
	sb.WriteString("So 100 means every domain agreeing at the top of its band, and a well-supported idea lands in the 50s–70s rather than the 30s. ")
	sb.WriteString(fmt.Sprintf("**Start from `base` and adjust by at most ±%d**, naming each adjustment. ", band))
	sb.WriteString("`covered` is the share of total domain weight behind the number; a low `covered` has already lowered `base`, ")
	sb.WriteString("so do not discount thin coverage a second time. `cap` is the further ceiling coverage imposes. ")
	sb.WriteString("`agree` is how much of the evidence's magnitude survived the domains disagreeing (100% = every domain that spoke pointed the same way). ")
	sb.WriteString("It is *already inside* `base` and is shown only so you can tell a thin-but-unanimous name from a well-covered name whose domains fought: ")
	sb.WriteString("both can land on the same `base`, and they are not the same idea. Do not adjust for it — it is not new evidence.\n\n")
	sb.WriteString("Per-domain cells are signed strengths (−10…+10); `·` means that domain had no data for the name.\n\n")
	sb.WriteString("| ticker | base dir | base | covered | agree | cap |")
	for _, d := range baseScoreDomains {
		sb.WriteString(" " + d + " |")
	}
	sb.WriteString("\n|---|---|---|---|---|---|")
	for range baseScoreDomains {
		sb.WriteString("---|")
	}
	sb.WriteString("\n")

	for _, b := range bases {
		dir := string(b.Direction)
		if dir == "" {
			dir = "—"
		}
		cap := "—"
		if b.Cap > 0 {
			cap = fmt.Sprintf("%d", b.Cap)
		}
		agree := "—"
		if b.CoveredWeight > 0 {
			agree = fmt.Sprintf("%.0f%%", b.Consensus*100)
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %d | %.0f%% | %s | %s |",
			b.Ticker, dir, b.Confidence, b.CoveredWeight*100, agree, cap))
		for _, d := range baseScoreDomains {
			if v, ok := b.Domains[d]; ok {
				sb.WriteString(fmt.Sprintf(" %+d |", v))
			} else {
				sb.WriteString(" · |")
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
