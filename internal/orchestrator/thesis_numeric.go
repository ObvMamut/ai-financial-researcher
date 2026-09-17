package orchestrator

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

var adsRatio = regexp.MustCompile(`(?i)(?:each|one|1)\s+(?:ADS|American depositary share)\s+(?:represents?|representing)\s+(one|two|four|eight|ten|[0-9]+(?:\.[0-9]+)?)\s+(?:class [a-z] )?(?:ordinary|common) shares?`)

func quotedADSRatio(passage string) float64 {
	m := adsRatio.FindStringSubmatch(passage)
	if len(m) < 2 {
		return 0
	}
	if v, ok := map[string]float64{"one": 1, "two": 2, "four": 4, "eight": 8, "ten": 10}[strings.ToLower(m[1])]; ok {
		return v
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

func normalizeComparison(ctx context.Context, c *model.NumericalComparison, ticker string, docs []model.EvidenceDocument, prices map[string]*quant.Series, fx *marketdata.FXRates, anchor time.Time) error {
	c.NormalizedTarget = nil
	c.UpsidePct = nil
	c.FX = nil
	c.Problem = ""
	fail := func(s string) error { c.Problem = s; return fmt.Errorf("%s", s) }
	if c.TargetTicker != ticker {
		return fail("comparison destination must be the researched listing")
	}
	src, srcUnit, err := marketdata.ResearchCurrency(c.SourceTicker)
	if err != nil {
		return fail(err.Error())
	}
	dst, dstUnit, err := marketdata.ResearchCurrency(c.TargetTicker)
	if err != nil {
		return fail(err.Error())
	}
	if src != c.SourceCurrency || dst != c.TargetCurrency || srcUnit != c.SourceUnit || dstUnit != c.TargetUnit {
		return fail("comparison currency or quotation unit does not match the listing")
	}
	if c.Target <= 0 || math.IsInf(c.Target, 0) || math.IsNaN(c.Target) {
		return fail("invalid numerical target")
	}
	published, err := time.Parse("2006-01-02", c.TargetPublishedOn)
	if err != nil || c.TargetPublishedOn > anchor.UTC().Format("2006-01-02") || c.TargetHorizon == "" {
		return fail("target requires a publication date at/before the anchor and an explicit forecast horizon")
	}
	if _, err = time.Parse("2006-01-02", c.PriceDate); err != nil || c.PriceDate > anchor.UTC().Format("2006-01-02") {
		return fail("invalid or future comparison price date")
	}
	if published.Format("2006-01-02") > c.PriceDate {
		return fail("target published after the comparison price; align valuation dates")
	}
	series := marketdata.CompletedDailySeries(prices[ticker], ticker, anchor)
	price := 0.0
	if series != nil {
		for _, bar := range series.Bars {
			if bar.Date == c.PriceDate {
				price = bar.Close
			}
		}
	}
	if price <= 0 {
		return fail("verified listing price unavailable on the comparison date")
	}
	basis := 1.0
	if c.SourceBasis != "share" && c.SourceBasis != "ADS" || c.TargetBasis != "share" && c.TargetBasis != "ADS" {
		return fail("explicit share/ADS bases required")
	}
	if c.SourceTicker == c.TargetTicker {
		if c.SourceBasis != c.TargetBasis {
			return fail("same listing cannot change share basis")
		}
	} else {
		local, ads := c.TargetTicker, c.SourceTicker
		if c.TargetBasis == "ADS" {
			local, ads = c.SourceTicker, c.TargetTicker
		}
		line, ok := marketdata.USLine(local)
		if !ok || line != ads || c.SourceBasis == c.TargetBasis {
			return fail("listings lack a verified local/ADS identity relationship")
		}
		effective, e := time.Parse("2006-01-02", c.RatioEffectiveOn)
		if e != nil || effective.After(published) || c.RatioEffectiveOn > c.PriceDate {
			return fail("ADS ratio effective date incompatible with valuation dates")
		}
		valid := false
		for _, doc := range docs {
			if ratioSourceValid(doc, *c, ticker, anchor) {
				valid = true
				break
			}
		}
		if !valid {
			return fail("missing or incompatible quoted ordinary-shares-per-ADS ratio")
		}
		basis = c.OrdinarySharesPerADS
		if c.SourceBasis == "ADS" {
			basis = 1 / basis
		}
	}
	// Value currencies on the reference-price date, not at today's exchange rate.
	valuation, _ := time.Parse("2006-01-02", c.PriceDate)
	valuation = valuation.Add(24*time.Hour - time.Nanosecond)
	if valuation.After(anchor) {
		valuation = anchor
	}
	conversion := 1.0
	if src != dst {
		a, e := fx.ResearchRate(ctx, src, valuation)
		if e != nil {
			return fail(e.Error())
		}
		b, e := fx.ResearchRate(ctx, dst, valuation)
		if e != nil {
			return fail(e.Error())
		}
		c.FX = []model.ResearchFX{a, b}
		conversion = a.USDPerUnit / b.USDPerUnit
	}
	unit := 1.0
	if srcUnit == "minor" {
		unit /= 100
	}
	if dstUnit == "minor" {
		unit *= 100
	}
	normalized := c.Target * basis * conversion * unit
	upside := (normalized/price - 1) * 100
	if math.IsInf(normalized, 0) || math.IsNaN(normalized) || math.IsInf(upside, 0) || math.IsNaN(upside) {
		return fail("non-finite normalized comparison")
	}
	c.NormalizedTarget = &normalized
	c.UpsidePct = &upside
	return nil
}

func (t *thesisRunner) validateComparisons(ctx context.Context, r *thesisResearch, prices map[string]*quant.Series) {
	for i := range r.Dossier.Claims {
		claim := &r.Dossier.Claims[i]
		if claim.Comparison == nil {
			continue
		}
		if err := normalizeComparison(ctx, claim.Comparison, r.Candidate.Ticker, r.Documents, prices, t.fx, t.run.TS); err != nil {
			r.Dossier.Unresolved = appendUnique(r.Dossier.Unresolved, "comparison "+claim.ID+": "+err.Error())
			r.Dossier.Status = "watchlist"
		}
	}
}

func ratioSourceValid(doc model.EvidenceDocument, c model.NumericalComparison, ticker string, anchor time.Time) bool {
	if doc.ID != c.RatioEvidenceID || doc.Ticker != ticker || doc.Error != "" || doc.Kind != "document" {
		return false
	}
	if doc.Authority != "issuer" && doc.Authority != "depositary" {
		return false
	}
	if doc.PublishedAt.After(anchor) {
		return false
	}
	dated := strings.Contains(c.RatioPassage, c.RatioEffectiveOn) || !doc.PublishedAt.IsZero() && doc.PublishedAt.Format("2006-01-02") == c.RatioEffectiveOn
	if !dated || len([]rune(c.RatioPassage)) < 30 || !strings.Contains(doc.Text, c.RatioPassage) {
		return false
	}
	return c.OrdinarySharesPerADS > 0 && quotedADSRatio(c.RatioPassage) == c.OrdinarySharesPerADS
}
