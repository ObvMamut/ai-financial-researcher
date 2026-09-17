package marketdata

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

var scriptText = regexp.MustCompile(`(?is)<(?:script|style|noscript)\b[^>]*>.*?</(?:script|style|noscript)>`)
var navigationText = regexp.MustCompile(`(?is)<(?:nav|footer)\b[^>]*>.*?</(?:nav|footer)>`)
var htmlTags = regexp.MustCompile(`(?s)<[^>]*>`)
var hrefs = regexp.MustCompile(`(?i)href\s*=\s*["']([^"']+)["']`)
var scriptStart = regexp.MustCompile(`(?is)<(?:script|style|noscript)\b`)

func cleanDocument(s string) string {
	text, _ := cleanDocumentBounded(s)
	return text
}

func cleanDocumentBounded(s string) (string, bool) {
	s = scriptText.ReplaceAllString(s, " ")
	s = navigationText.ReplaceAllString(s, " ")
	// A bounded prefix may end inside a script. Drop its tail before removing
	// tags so executable text cannot be mistaken for article prose.
	if loc := scriptStart.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	s = html.UnescapeString(htmlTags.ReplaceAllString(s, " "))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 40000 {
		return string(r[:40000]), true
	}
	return s, false
}
func EvidenceID(ticker, source, text string) string {
	h := sha256.Sum256([]byte(ticker + "\n" + source + "\n" + text))
	return fmt.Sprintf("ev-%x", h[:10])
}

// EvidenceFromPack preserves provider as-of semantics in Text: as_of is not
// necessarily publication time (quarterly holdings often carry fetch time).
func EvidenceFromPack(p *DataPack, ticker string, now time.Time) []model.EvidenceDocument {
	if p == nil {
		return nil
	}
	var out []model.EvidenceDocument
	for _, f := range p.ByTicker[ticker].Facts {
		kind := "fact"
		body := f.Value
		var published time.Time
		if strings.HasPrefix(f.Label, "Headline ") {
			kind = "headline"
			published = f.AsOf
		}
		if f.Summary != "" {
			kind = "summary"
			body += "\nSummary: " + f.Summary
		}
		if f.Content != "" {
			kind = "document"
			body += "\nContent: " + f.Content
		}
		if strings.Contains(strings.ToLower(f.Source), "computed") {
			kind = "computed"
		}
		if !f.AsOf.IsZero() {
			body += "\nProvider as_of (not necessarily event or publication time): " + f.AsOf.Format(time.RFC3339)
		}
		out = append(out, model.EvidenceDocument{ID: EvidenceID(ticker, f.URL, f.Label+body), Ticker: ticker, URL: f.URL, Title: f.Label, Kind: kind, Text: body, PublishedAt: published, RetrievedAt: now, Source: f.Source})
	}
	return out
}

// DocumentReader fetches public source documents without sending provider keys.
// Requests are bounded; redirects and DNS are checked to prevent a model URL
// from reaching local services. An injected client is used only by fixture tests.
type DocumentReader struct {
	Client  *http.Client
	Contact string
}

func publicURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("not a public HTTP URL")
	}
	if u.Port() != "" && u.Port() != "80" && u.Port() != "443" {
		return fmt.Errorf("non-public port")
	}
	return nil
}
func publicIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}
func publicClient() *http.Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		for _, a := range ips {
			if !publicIP(a.IP) {
				return nil, fmt.Errorf("non-public destination")
			}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("empty DNS answer")
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}}
	return &http.Client{Transport: tr, Timeout: 25 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return publicURL(r.URL.String())
	}}
}
func (r DocumentReader) Read(ctx context.Context, ticker, raw string) model.EvidenceDocument {
	d := model.EvidenceDocument{Ticker: ticker, URL: raw, Title: raw, Kind: "document", RetrievedAt: time.Now().UTC()}
	fail := func(e error) model.EvidenceDocument {
		d.Error = e.Error()
		d.ID = EvidenceID(ticker, raw, d.Error)
		return d
	}
	if e := publicURL(raw); e != nil {
		return fail(e)
	}
	client := r.Client
	if client == nil {
		client = publicClient()
	}
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return fail(e)
	}
	req.Header.Set("User-Agent", "CFR research contact "+r.Contact)
	resp, e := client.Do(req)
	if e != nil {
		return fail(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fail(fmt.Errorf("source HTTP %d", resp.StatusCode))
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if e != nil {
		return fail(e)
	}
	if len(b) > 2<<20 {
		d.Truncated = true
		b = b[:2<<20]
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "pdf") || strings.HasPrefix(string(b), "%PDF") {
		return fail(fmt.Errorf("PDF text unavailable; request an accessible HTML release or filing exhibit"))
	}
	if ct != "" && !strings.Contains(ct, "text") && !strings.Contains(ct, "json") && !strings.Contains(ct, "xml") {
		return fail(fmt.Errorf("unsupported document type"))
	}
	textBody := string(b)
	if d.Truncated {
		// Discard an unfinished tag; only complete markup from the bounded
		// response prefix contributes text or links.
		if open := strings.LastIndex(textBody, "<"); open > strings.LastIndex(textBody, ">") {
			textBody = textBody[:open]
		}
	}
	var textTruncated bool
	d.Text, textTruncated = cleanDocumentBounded(textBody)
	d.Truncated = d.Truncated || textTruncated
	if len(d.Text) < 80 {
		return fail(fmt.Errorf("no readable document text"))
	}
	base := resp.Request.URL
	d.NavigationOnly = ResearchNavigationURL(base.String())
	d.Source = base.Hostname()
	seen := map[string]bool{}
	for _, m := range hrefs.FindAllStringSubmatch(textBody, -1) {
		u, e := url.Parse(html.UnescapeString(m[1]))
		if e != nil {
			continue
		}
		s := base.ResolveReference(u).String()
		if publicURL(s) == nil && !seen[s] {
			seen[s] = true
			d.Links = append(d.Links, s)
		}
	}
	d.Links = RankResearchLinks(d.Links, base.String())
	if len(d.Links) > 80 {
		d.Links = d.Links[:80]
	}
	d.ID = EvidenceID(ticker, raw, d.Text)
	return d
}

// LoadResearchSources merges the bundled public issuer registry with an optional
// ticker,url CSV. URLs are discovery seeds, not evidence until actually read.
func LoadResearchSources(path string) (map[string][]string, error) {
	out := map[string][]string{}
	read := func(b io.Reader) error {
		rows, e := csv.NewReader(b).ReadAll()
		if e != nil {
			return e
		}
		for _, r := range rows {
			if len(r) != 2 {
				return fmt.Errorf("sources require ticker,url")
			}
			if r[0] == "ticker" {
				continue
			}
			if e := publicURL(r[1]); e != nil {
				return e
			}
			t := strings.ToUpper(strings.TrimSpace(r[0]))
			out[t] = append(out[t], r[1])
		}
		return nil
	}
	if e := read(strings.NewReader(issuerSources)); e != nil {
		return nil, e
	}
	if path != "" {
		f, e := os.Open(path)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		if e = read(f); e != nil {
			return nil, e
		}
	}
	return out, nil
}

// ResearchFilings exposes recent source links, including earnings-release
// exhibits linked from 8-K/6-K. Filing dates are explicitly NOT earnings dates.
func ResearchFilings(ctx context.Context, contact string, cache *Cache, ticker string) ([]model.EvidenceDocument, error) {
	if contact == "" {
		return nil, fmt.Errorf("SEC contact_email unavailable")
	}
	p := NewEdgarProvider(contact, cache).(*edgarProvider)
	p.ensureCIKMap(ctx)
	cik, ok := p.lookupCIK(ticker)
	if !ok {
		return nil, ErrNotApplicable
	}
	fs, e := p.recentFilingsAny(ctx, cik, []string{"8-K", "6-K", "10-Q", "10-K", "20-F"}, 100, 8)
	if e != nil {
		return nil, e
	}
	var out []model.EvidenceDocument
	for _, f := range fs {
		body := fmt.Sprintf("%s filed %s. This is the filing date, not the earnings announcement time. Open the filing and its release exhibit to identify the event.", f.form, f.filed.Format("2006-01-02"))
		out = append(out, model.EvidenceDocument{ID: EvidenceID(ticker, f.url, body), Ticker: ticker, URL: f.url, Title: f.form, Kind: "fact", Text: body, PublishedAt: f.filed, RetrievedAt: time.Now().UTC()})
	}
	return out, nil
}

func DeduplicateEvidence(ds []model.EvidenceDocument) []model.EvidenceDocument {
	seen := map[string]bool{}
	out := make([]model.EvidenceDocument, 0, len(ds))
	for _, d := range ds {
		if !seen[d.ID] {
			seen[d.ID] = true
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
