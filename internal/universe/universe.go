// Package universe loads and queries the tradeable index constituents.
package universe

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

//go:embed data/*.csv
var dataFS embed.FS

// frozenFS holds byte-identical copies of the index files as a registered
// out-of-sample test fixed them, one directory per registration. The live
// samples keep changing; a registration's universe must not.
//
//go:embed data/frozen/oos-h1-63/*.csv
var frozenFS embed.FS

// indexKeys is the ordered set of supported index identifiers.
var indexKeys = []string{"sp500", "nq100", "eu50", "asia100"}

// Universe holds all loaded constituents indexed by key.
type Universe struct {
	byIndex  map[string][]model.Constituent
	byTicker map[string]model.Constituent // canonical ticker → first match
	frozen   string                       // registration name; "" for the live samples
}

// Load reads all four CSV files from the embedded FS and returns a Universe.
func Load() (*Universe, error) {
	return load(dataFS, "data", "")
}

// LoadFrozen reads the four index files frozen for one registration (for
// example "oos-h1-63") instead of the live samples. An unknown name is an
// error, never an empty universe.
func LoadFrozen(name string) (*Universe, error) {
	dir := "data/frozen/" + name
	if st, err := fs.Stat(frozenFS, dir); name == "" || strings.Contains(name, "/") || err != nil || !st.IsDir() {
		return nil, fmt.Errorf("universe: no frozen universe %q", name)
	}
	return load(frozenFS, dir, name)
}

// Frozen names the registration this universe was frozen for, or "" when it
// holds the live samples.
func (u *Universe) Frozen() string { return u.frozen }

func load(fsys fs.FS, dir, frozen string) (*Universe, error) {
	u := &Universe{
		byIndex:  make(map[string][]model.Constituent),
		byTicker: make(map[string]model.Constituent),
		frozen:   frozen,
	}
	for _, key := range indexKeys {
		path := fmt.Sprintf("%s/%s.csv", dir, key)
		f, err := fsys.Open(path)
		if err != nil {
			return nil, fmt.Errorf("universe: open %s: %w", path, err)
		}
		rows, err := parseCSV(f, key)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("universe: parse %s: %w", path, err)
		}
		u.byIndex[key] = rows
		for _, c := range rows {
			if _, exists := u.byTicker[c.Ticker]; !exists {
				u.byTicker[c.Ticker] = c
			}
		}
	}
	return u, nil
}

// Constituents returns all members of one index, or nil if the key is unknown.
func (u *Universe) Constituents(indexKey string) []model.Constituent {
	return u.byIndex[indexKey]
}

// AllIndices returns the list of valid index keys.
func AllIndices() []string { return indexKeys }

// benchmarkSymbols maps index keys to Yahoo Finance benchmark symbols used for
// beta/correlation in the quant pack.
var benchmarkSymbols = map[string]string{
	"sp500":   "^GSPC",
	"nq100":   "^NDX",
	"eu50":    "^STOXX50E",
	"asia100": "^N225",
}

// marketBenchmarks maps an exchange suffix to the index a listing on that
// exchange actually moves with.
//
// It exists for asia100. The other three index keys name a real index their
// members belong to — ^GSPC, ^NDX and ^STOXX50E are each a single tradeable
// market — but "asia100" is this project's own grouping across eight exchanges
// with no common index, and it was benchmarked entirely against ^N225. So
// O39.SI, a Singapore bank, had its beta, its correlation and its displayed RS63
// computed against the Nikkei: three numbers that describe the relationship
// between Singaporean banking and Japanese equities, presented as a read on the
// stock.
//
// Only the suffixes asia100 actually contains are listed; an unlisted one falls
// back to the index default, which is the previous behaviour.
var marketBenchmarks = map[string]string{
	"T":  "^N225",   // Tokyo
	"HK": "^HSI",    // Hong Kong
	"NS": "^NSEI",   // India, NSE
	"AX": "^AXJO",   // Australia, ASX 200
	"TW": "^TWII",   // Taiwan
	"KS": "^KS11",   // Korea, KOSPI
	"SI": "^STI",    // Singapore
	"BK": "^SET.BK", // Thailand
}

// multiMarketIndices are the index keys whose members span exchanges with no
// index in common, and which therefore have to be benchmarked per listing.
var multiMarketIndices = map[string]bool{"asia100": true}

// BenchmarkSymbol returns the Yahoo benchmark symbol for an index key, falling
// back to ^GSPC for unknown or empty keys. Prefer BenchmarkFor where the ticker
// is known: this answers for the index as a whole.
func BenchmarkSymbol(indexKey string) string {
	if s, ok := benchmarkSymbols[indexKey]; ok {
		return s
	}
	return "^GSPC"
}

// BenchmarkFor returns the benchmark one listing should be measured against.
// For a single-market index that is the index's own benchmark; for asia100 it is
// the ticker's own exchange index. See marketBenchmarks.
func BenchmarkFor(indexKey, ticker string) string {
	if !multiMarketIndices[indexKey] {
		return BenchmarkSymbol(indexKey)
	}
	if i := strings.LastIndex(ticker, "."); i > 0 {
		if s, ok := marketBenchmarks[strings.ToUpper(ticker[i+1:])]; ok {
			return s
		}
	}
	return BenchmarkSymbol(indexKey)
}

// splitSuffix returns the ticker stem and whether a known exchange suffix was
// stripped (ASML.AS → "ASML", true; BRK.B → "BRK.B", false).
//
// The suffix set lives in marketdata, which owns the question of whether a
// symbol is a foreign listing. This package used to keep its own 18-entry copy
// beside marketdata's 56-entry one; the two agreed on today's data by luck, and
// the first universe row added with a .TO or .SS suffix would have been rejected
// by the providers while still failing to dedupe against its US cross-listing.
func splitSuffix(ticker string) (string, bool) {
	i := strings.LastIndex(ticker, ".")
	if i <= 0 {
		return ticker, false
	}
	if marketdata.IsForeignSuffix(ticker[i+1:]) {
		return ticker[:i], true
	}
	return ticker, false
}

// sameCompany is a conservative name match used to confirm a cross-listing.
// Stems collide across unrelated companies (SAN.MC Santander vs SAN.PA Sanofi,
// 2382.HK vs 2382.TW), so a stem match alone is never enough.
func sameCompany(a, b string) bool {
	ta, tb := firstToken(a), firstToken(b)
	return ta != "" && ta == tb
}

func firstToken(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for i, r := range name {
		if r == ' ' || r == '.' || r == ',' {
			return name[:i]
		}
	}
	return name
}

// Dedupe merges overlapping shortlists from multiple scouts. Exact ticker
// repeats collapse to the first-seen Candidate. Cross-listings of the same
// company (same stem under a known exchange suffix + matching company name,
// e.g. ASML and ASML.AS) also collapse, preferring the unsuffixed listing.
// Insertion order is preserved.
//
// A merge is not a discard. Two scouts nominating the same name in the same
// direction is the strongest agreement this stage produces, and two nominating
// it in opposite directions is the loudest disagreement; both used to vanish
// into "keep whichever came first". The counts go on the surviving Candidate —
// Nominations and Contested — for the merit sort and the run's warnings to read.
func Dedupe(candidates []model.Candidate) []model.Candidate {
	out := make([]model.Candidate, 0, len(candidates))
	// at reports where an already-merged candidate for this name lives.
	at := func(k string, c model.Candidate) int {
		stem, suffixed := splitSuffix(k)
		for j := range out {
			ok := strings.ToUpper(out[j].Ticker)
			if ok == k {
				return j
			}
			ostem, osuffixed := splitSuffix(ok)
			if ostem != stem || !sameCompany(c.Name, out[j].Name) {
				continue
			}
			// A cross-listing of the same company: prefer the primary listing's
			// symbol but keep the tallies already on the row.
			if osuffixed && !suffixed {
				kept := out[j]
				c.Nominations, c.Contested, c.NominatedBy = kept.Nominations, kept.Contested, kept.NominatedBy
				out[j] = c
			}
			return j
		}
		return -1
	}

	for _, c := range candidates {
		k := strings.ToUpper(c.Ticker)
		if c.Nominations == 0 {
			c.Nominations = 1
		}
		if c.Index != "" && !containsString(c.NominatedBy, c.Index) {
			c.NominatedBy = append(c.NominatedBy, c.Index)
		}
		j := at(k, c)
		if j < 0 {
			out = append(out, c)
			continue
		}
		if out[j].Bias != c.Bias {
			// Opposite readings of the same name. The first-seen one stays —
			// there is no basis here for preferring either — but the collision
			// is recorded so the orchestrator can warn and the merit sort can
			// see the name is contested rather than agreed.
			if from := c.Index; from != "" && !containsString(out[j].Contested, from) {
				out[j].Contested = append(out[j].Contested, from)
			}
			continue
		}
		out[j].Nominations += c.Nominations
		for _, idx := range c.NominatedBy {
			if !containsString(out[j].NominatedBy, idx) {
				out[j].NominatedBy = append(out[j].NominatedBy, idx)
			}
		}
	}
	return out
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// MeritCaps are the constraints CapMerit trims a merged shortlist under.
type MeritCaps struct {
	// Max is the size of the shortlist. Zero or less returns the input untouched.
	Max int
	// PerIndex caps one index's share of the shortlist, before the merit
	// backfill. It is a diversification preference: the backfill overrides it
	// rather than hand back a five-name shortlist when twelve were asked for.
	PerIndex int
	// ThinlyCovered caps the names whose expected domain coverage falls below
	// CoverageFloor — the ones the run can only partly grade. Unlike PerIndex
	// this one is hard, in both passes: a soft cap here would be no cap at all,
	// since the backfill would refill exactly the slots the first pass had
	// protected.
	//
	// Zero admits none of them, which is the useful default rather than an
	// edge case: a name below the floor is one only the price-derived domains
	// can score, and the risk gate's evidence floor deletes a price-only idea
	// outright. Reserving shortlist slots for such names spends five specialist
	// reports on candidates that cannot reach the output. A **negative** value
	// disables the cap, as does a nil Coverage.
	ThinlyCovered int
	// CoverageFloor is the share of total domain weight below which a name
	// counts as thinly covered. Zero disables the cap with ThinlyCovered.
	CoverageFloor float64
	// PerSector caps one sector's share of the shortlist. Like PerIndex it is a
	// diversification preference the backfill overrides rather than hand back a
	// short list, and for the same reason: a thin shortlist is worse than a
	// concentrated one, because the stage that picks the book can decline a
	// crowded sector but cannot conjure a name that never reached it.
	//
	// It exists because the risk gate already refuses a book with more than two
	// ideas in one sector, and nothing upstream knew that. On 2026-09-05 the
	// merit sort — which has no notion of sector at all — returned a shortlist
	// that was seven-twelfths Information Technology, seven of the eight names
	// that then cleared the evidence floor were IT, and the gate's own limit cut
	// the book to two. The funnel was maximising exactly the quantity the gate
	// forbids. Zero or less disables it.
	PerSector int
	// Reserve is how many of Max's slots are held for candidates ReservePredicate
	// matches, before the ordinary passes fill the rest. Zero disables it.
	//
	// It exists because the merit sort ranks on the pre-screen composite, and
	// that composite is built from trailing returns — so left to itself it
	// hands every slot to whatever has run hardest. On 2026-09-04 that was
	// three longs at 0.993, 0.982 and 1.000 of their 52-week highs. Reserving
	// slots for the other setup archetypes is what keeps the shortlist from
	// collapsing back onto one shape once the pre-screen has gone to the
	// trouble of finding several.
	//
	// The reserve is *soft in the only direction that matters*: it can leave
	// the shortlist short but it cannot pad it. A reserved slot is filled only
	// by a candidate that also clears ReserveMinMerit, so a run with no decent
	// pullback ships fewer names rather than a bad one — the same bargain
	// ThinlyCovered strikes above.
	Reserve int
	// ReservePredicate selects the candidates eligible for a reserved slot. Nil
	// disables the reserve. This package holds no opinion about what deserves
	// reserving; the orchestrator passes the archetype test.
	ReservePredicate func(model.Candidate) bool
	// ReserveMinMerit is the Score a candidate must reach to take a reserved
	// slot. Without it the reserve would guarantee three names of some other
	// shape whatever their quality, which trades one bad selection rule for
	// another.
	ReserveMinMerit float64
	// Score ranks a candidate. Supplied by the caller (the orchestrator aligns
	// each candidate's pre-screen composite with the direction it was nominated
	// in), which keeps this package free of scoring policy.
	Score func(model.Candidate) float64
	// Coverage reports the share of total domain weight the run's sources can
	// actually ground for a name. The caller's to answer: this package knows
	// about index membership, not about which endpoints a run has keys for or
	// which of them reach a given listing.
	//
	// It replaced a boolean "can any per-ticker provider reach this at all",
	// which stopped being the right question once the domains reached listings
	// unevenly. A name with a global news source and a global regime read is not
	// in the same position as one with neither, and calling both "quant-only"
	// hid the difference — it also made the cap answer in a unit unrelated to
	// the base score the shortlist is eventually graded on.
	Coverage func(model.Candidate) float64
}

func (c MeritCaps) thinlyCovered(cand model.Candidate) bool {
	return c.ThinlyCovered >= 0 && c.CoverageFloor > 0 && c.Coverage != nil &&
		c.Coverage(cand) < c.CoverageFloor
}

// CapMerit trims the shortlist to at most caps.Max names, keeping the
// highest-scoring candidates under the caps above.
//
// It replaces the old round-robin cap, which took each index's candidates in the
// order its scout happened to emit them. That treated "first name the model
// typed" as a ranking, so a strongly-supported nomination could be dropped for a
// throwaway one from another index.
//
// Ranking on the composite alone had no notion of whether the run's providers
// could reach a name. On 2026-09-01 that put 7 names the research layer could
// barely see on a shortlist of 12, and three of the five shipped ideas rested on
// a single domain; the arithmetic even let the lone domain outrank a consensus,
// O39.SI's one `quant 8` scoring 36 against ORCL's five-domain 26. ThinlyCovered
// bounds how much of the shortlist can be evidence the run cannot gather, in the
// same unit the base score is computed in. The pre-screen ranking is untouched —
// this only decides who reaches the specialists.
//
// Two passes. The first respects PerIndex and PerSector, which are what spread
// the book across regions and industries. The second fills any slots those caps
// left empty, in pure score order. Ties keep their input order.
//
// The result is always ordered best-first, including when nothing needed
// trimming: everything downstream — the shortlist block each specialist reads,
// the Chief's ranking prompt, shortlist.json — is more useful ranked than in
// whatever order the scouts were collected in.
func CapMerit(candidates []model.Candidate, caps MeritCaps) []model.Candidate {
	if caps.Max <= 0 {
		return candidates
	}
	score := caps.Score
	if score == nil {
		score = func(model.Candidate) float64 { return 0 }
	}
	ranked := make([]model.Candidate, len(candidates))
	copy(ranked, candidates)
	sort.SliceStable(ranked, func(i, j int) bool { return score(ranked[i]) > score(ranked[j]) })

	out := make([]model.Candidate, 0, caps.Max)
	taken := make([]bool, len(ranked))
	perIndex := map[string]int{}
	perSector := map[string]int{}
	thin := 0

	// Pass 0: the archetype reserve. It runs first because a slot held back
	// after the fact is not held back at all — the ordinary passes below fill
	// to Max, and there would be nothing left to reserve. Every other cap still
	// binds here, so a reserved name cannot smuggle a run past PerIndex or
	// ThinlyCovered.
	if caps.Reserve > 0 && caps.ReservePredicate != nil {
		reserved := 0
		for i, c := range ranked {
			if reserved == caps.Reserve || len(out) == caps.Max {
				break
			}
			if !caps.ReservePredicate(c) || score(c) < caps.ReserveMinMerit {
				continue
			}
			if caps.PerIndex > 0 && perIndex[c.Index] >= caps.PerIndex {
				continue
			}
			if caps.PerSector > 0 && c.Sector != "" && perSector[c.Sector] >= caps.PerSector {
				continue
			}
			if caps.thinlyCovered(c) {
				if thin >= caps.ThinlyCovered {
					continue
				}
				thin++
			}
			perIndex[c.Index]++
			perSector[c.Sector]++
			taken[i] = true
			out = append(out, c)
			reserved++
		}
	}

	for i, c := range ranked {
		if taken[i] {
			continue
		}
		if len(out) == caps.Max {
			break
		}
		if caps.PerIndex > 0 && perIndex[c.Index] >= caps.PerIndex {
			continue
		}
		if caps.PerSector > 0 && c.Sector != "" && perSector[c.Sector] >= caps.PerSector {
			continue
		}
		if caps.thinlyCovered(c) {
			if thin >= caps.ThinlyCovered {
				continue
			}
			thin++
		}
		perIndex[c.Index]++
		perSector[c.Sector]++
		taken[i] = true
		out = append(out, c)
	}
	for i, c := range ranked {
		if len(out) == caps.Max {
			break
		}
		if taken[i] {
			continue
		}
		if caps.thinlyCovered(c) {
			if thin >= caps.ThinlyCovered {
				continue
			}
			thin++
		}
		out = append(out, c)
	}
	// Re-rank what the three passes selected. Each pass appends in score order
	// within itself, but a name a later pass rescued can outscore one an
	// earlier pass took — and with the reserve running first, usually does.
	// Selection order is not presentation order, and everything downstream
	// reads this list as a ranking.
	sort.SliceStable(out, func(i, j int) bool { return score(out[i]) > score(out[j]) })
	return out
}

// Lookup resolves a user-entered ticker string.
// Returns the matching Constituent and true, or a best-effort off-universe
// placeholder and false when the ticker is not in any tracked index.
func (u *Universe) Lookup(ticker string) (model.Constituent, bool) {
	norm := strings.ToUpper(strings.TrimSpace(ticker))
	if c, ok := u.byTicker[norm]; ok {
		return c, true
	}
	// Try case-insensitive scan for exchange-suffixed tickers like "asml.as"
	for k, c := range u.byTicker {
		if strings.EqualFold(k, norm) {
			return c, true
		}
	}
	return model.Constituent{Ticker: norm, Name: norm}, false
}

// ConstituentList returns a compact text representation of constituents for use
// in agent prompts (one line per ticker).
func ConstituentList(cs []model.Constituent) string {
	var sb strings.Builder
	for _, c := range cs {
		sb.WriteString(c.Ticker)
		sb.WriteString(" | ")
		sb.WriteString(c.Name)
		sb.WriteString(" | ")
		sb.WriteString(c.Sector)
		sb.WriteString(" | ")
		sb.WriteString(c.Exchange)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// parseCSV reads ticker,name,exchange,country,sector rows; skips comments and header.
func parseCSV(f fs.File, indexKey string) ([]model.Constituent, error) {
	scanner := bufio.NewScanner(f)
	var rows []model.Constituent
	// A ticker listed twice in one file is a data-entry slip, and it does not stay
	// harmless: nq100.csv carried PDD twice, so the pre-screen fetched and ranked
	// it twice, counted it twice in that index's z-score mean and standard
	// deviation, and could hand the scout two identical rows out of the fifteen it
	// sees. The first row wins; a later one is dropped.
	seen := make(map[string]bool)
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if first {
			first = false
			if strings.HasPrefix(strings.ToLower(line), "ticker") {
				continue // skip header
			}
		}
		parts := strings.SplitN(line, ",", 6)
		if len(parts) < 5 {
			continue
		}
		ticker := strings.TrimSpace(parts[0])
		if key := strings.ToUpper(ticker); key == "" || seen[key] {
			continue
		} else {
			seen[key] = true
		}
		rows = append(rows, model.Constituent{
			Ticker:   ticker,
			Name:     strings.TrimSpace(parts[1]),
			Exchange: strings.TrimSpace(parts[2]),
			Country:  strings.TrimSpace(parts[3]),
			Sector:   strings.TrimSpace(parts[4]),
			Index:    indexKey,
		})
	}
	return rows, scanner.Err()
}

// Contains reports whether one index holds a ticker. It answers the question
// the merge needs and Lookup cannot: Lookup is keyed by ticker alone and returns
// the first index a name was loaded under, so it cannot say that MU is in both
// sp500 and nq100 — which is exactly what decides whether two scouts nominating
// it were reading two candidate pools or one.
func (u *Universe) Contains(indexKey, ticker string) bool {
	want := strings.ToUpper(strings.TrimSpace(ticker))
	for _, c := range u.byIndex[indexKey] {
		if strings.ToUpper(c.Ticker) == want {
			return true
		}
	}
	return false
}
