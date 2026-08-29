// Package universe loads and queries the tradeable index constituents.
package universe

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

//go:embed data/*.csv
var dataFS embed.FS

// indexKeys is the ordered set of supported index identifiers.
var indexKeys = []string{"sp500", "nq100", "eu50", "asia100"}

// Universe holds all loaded constituents indexed by key.
type Universe struct {
	byIndex  map[string][]model.Constituent
	byTicker map[string]model.Constituent // canonical ticker → first match
}

// Load reads all four CSV files from the embedded FS and returns a Universe.
func Load() (*Universe, error) {
	u := &Universe{
		byIndex:  make(map[string][]model.Constituent),
		byTicker: make(map[string]model.Constituent),
	}
	for _, key := range indexKeys {
		path := fmt.Sprintf("data/%s.csv", key)
		f, err := dataFS.Open(path)
		if err != nil {
			return nil, fmt.Errorf("universe: open %s: %w", path, err)
		}
		rows, err := parseCSV(f.(fs.File), key)
		f.(fs.File).Close()
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

// BenchmarkSymbol returns the Yahoo benchmark symbol for an index key, falling
// back to ^GSPC for unknown or empty keys.
func BenchmarkSymbol(indexKey string) string {
	if s, ok := benchmarkSymbols[indexKey]; ok {
		return s
	}
	return "^GSPC"
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
func Dedupe(candidates []model.Candidate) []model.Candidate {
	seen := make(map[string]bool, len(candidates))
	out := make([]model.Candidate, 0, len(candidates))
	for _, c := range candidates {
		k := strings.ToUpper(c.Ticker)
		if seen[k] {
			continue
		}
		seen[k] = true
		stem, suffixed := splitSuffix(k)
		merged := false
		for j := range out {
			ostem, osuffixed := splitSuffix(strings.ToUpper(out[j].Ticker))
			if ostem != stem || !sameCompany(c.Name, out[j].Name) {
				continue
			}
			if osuffixed && !suffixed {
				out[j] = c // replace suffixed listing with the primary one
			}
			merged = true
			break
		}
		if !merged {
			out = append(out, c)
		}
	}
	return out
}

// CapBalanced trims the shortlist to at most max names, round-robin across the
// candidates' source indices so no single index dominates. Order within an
// index is preserved; candidates without an Index share one group.
func CapBalanced(candidates []model.Candidate, max int) []model.Candidate {
	if max <= 0 || len(candidates) <= max {
		return candidates
	}
	groups := make(map[string][]model.Candidate)
	var order []string
	for _, c := range candidates {
		if _, ok := groups[c.Index]; !ok {
			order = append(order, c.Index)
		}
		groups[c.Index] = append(groups[c.Index], c)
	}
	out := make([]model.Candidate, 0, max)
	for len(out) < max {
		progressed := false
		for _, k := range order {
			if len(groups[k]) == 0 {
				continue
			}
			out = append(out, groups[k][0])
			groups[k] = groups[k][1:]
			progressed = true
			if len(out) == max {
				break
			}
		}
		if !progressed {
			break
		}
	}
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
		rows = append(rows, model.Constituent{
			Ticker:   strings.TrimSpace(parts[0]),
			Name:     strings.TrimSpace(parts[1]),
			Exchange: strings.TrimSpace(parts[2]),
			Country:  strings.TrimSpace(parts[3]),
			Sector:   strings.TrimSpace(parts[4]),
			Index:    indexKey,
		})
	}
	return rows, scanner.Err()
}
