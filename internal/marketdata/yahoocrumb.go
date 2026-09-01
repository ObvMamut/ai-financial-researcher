package marketdata

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"strings"
	"sync"
	"time"
)

// yahooAuth performs the cookie+crumb handshake Yahoo's quote-family endpoints
// require, and holds the result for the life of the process.
//
// The option chain has been returning 401 on every request. The comment on the
// provider called the endpoint "intermittently crumb-gated" and treated a 401 as
// an expected degradation — but /v7/finance/options/ now requires the crumb
// unconditionally, so the degraded path was the only path. The 2026-09-01 run
// failed all eight tickers, and the sentiment reports say so on every line: "no
// options data", "IV/RV not available", "strength is capped without options
// confirmation". The whole reason the provider exists is to give sentiment
// evidence the news domain is not already reading, and it was contributing
// nothing on any run.
//
// The handshake is two requests: fetch a consent cookie, then exchange it for a
// crumb. Both are keyless. The crumb is stable for hours, so this is a fixed
// cost per process, not per ticker.
type yahooAuth struct {
	client  *http.Client
	baseURL string
	// skip is set when the base URL has been rerouted (CFR_YAHOO_BASE): a
	// fixture server or mirror does not gate, and has no /v1/test/getcrumb to
	// answer with. Asking it would fail every hermetic test for no reason.
	skip bool

	mu    sync.Mutex
	crumb string
	got   time.Time
	err   error
}

// crumbTTL is how long a crumb is reused before the handshake runs again.
// Yahoo's crumbs outlive this comfortably; the point of expiring at all is that
// a process left running overnight re-authenticates rather than 401ing forever.
const crumbTTL = 2 * time.Hour

// cookieURL is the host that issues the consent cookie. It answers 404, which is
// fine — the response headers are the point.
const cookieURL = "https://fc.yahoo.com/"

func newYahooAuth(baseURL string, rerouted bool) *yahooAuth {
	jar, _ := cookiejar.New(nil)
	return &yahooAuth{
		client:  &http.Client{Timeout: 20 * time.Second, Jar: jar},
		baseURL: baseURL,
		skip:    rerouted,
	}
}

// Crumb returns the current crumb, running the handshake if needed. `refresh`
// discards a cached crumb first, which is what a 401 on an otherwise valid
// request means.
//
// It returns ("", nil) when there is nothing to authenticate against — a
// rerouted base — so callers can append the crumb unconditionally and get an
// unchanged URL in tests.
func (a *yahooAuth) Crumb(ctx context.Context, refresh bool) (string, error) {
	if a == nil || a.skip {
		return "", nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if refresh {
		a.crumb, a.err, a.got = "", nil, time.Time{}
	}
	if a.crumb != "" && time.Since(a.got) < crumbTTL {
		return a.crumb, nil
	}
	// A failed handshake is remembered for the same TTL: if Yahoo is refusing,
	// re-running two requests per ticker will not change its mind.
	if a.err != nil && time.Since(a.got) < crumbTTL {
		return "", a.err
	}

	crumb, err := a.handshake(ctx)
	a.crumb, a.err, a.got = crumb, err, time.Now()
	if err != nil {
		return "", err
	}
	return crumb, nil
}

func (a *yahooAuth) handshake(ctx context.Context) (string, error) {
	// 1. Collect the consent cookie. The status does not matter; the Set-Cookie
	//    header does, and the jar keeps it for the finance hosts.
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, cookieURL, nil); err == nil {
		req.Header.Set("User-Agent", yahooBrowserUA)
		if resp, err := a.client.Do(req); err == nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
			resp.Body.Close()
		}
	}

	// 2. Exchange it for a crumb. The body is the crumb itself, in plain text.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/v1/test/getcrumb", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", yahooBrowserUA)
	req.Header.Set("Accept", "text/plain")
	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: yahoo crumb: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: yahoo crumb: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return "", fmt.Errorf("%w: yahoo crumb: %v", ErrUnavailable, err)
	}
	crumb := strings.TrimSpace(string(body))
	// An HTML body means a consent wall rather than a crumb, and using it as one
	// produces an opaque 401 later instead of a legible failure now.
	if crumb == "" || strings.ContainsAny(crumb, "<> \t\n") {
		return "", fmt.Errorf("%w: yahoo crumb: no usable crumb in the response", ErrUnavailable)
	}
	return crumb, nil
}

// Do issues req with the crumb appended and the jar's cookies attached,
// retrying once with a fresh crumb if the first attempt is refused. A 401 is
// exactly what a stale crumb looks like, so one retry is the difference between
// working and never working.
func (a *yahooAuth) Do(ctx context.Context, url string) (*http.Response, error) {
	resp, err := a.do(ctx, url, false)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	resp.Body.Close()
	return a.do(ctx, url, true)
}

func (a *yahooAuth) do(ctx context.Context, url string, refresh bool) (*http.Response, error) {
	if crumb, err := a.Crumb(ctx, refresh); err == nil && crumb != "" {
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		url += sep + "crumb=" + neturl.QueryEscape(crumb)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", yahooBrowserUA)
	req.Header.Set("Accept", "application/json")
	return a.client.Do(req)
}
