// Package redact keeps configured credentials out of everything a run persists,
// prompts a model with, or prints.
//
// It exists because a provider can echo the request it was sent. AlphaVantage
// answers a rejected call with prose that quotes the whole query string —
// apikey included — and Go's own http.Client wraps a transport failure in a
// *url.Error carrying the same URL. Both land in DataPack.Errors, from there in
// metadata.json, in the retrieval diagnostics block of a researcher prompt, and
// in the run log. Nothing in that path was looking at the string.
//
// The registry is process-global and append-only on purpose: the value that
// must never be written is a property of the process's configuration, not of
// whichever call site happens to be holding a string. Registration happens once
// where credentials are read; every sink then redacts without needing to know
// which provider produced the text.
package redact

import (
	"net/url"
	"strings"
	"sync"
)

// Placeholder replaces a credential wherever one is found. It is deliberately
// visible: a reader of a sanitized artifact must be able to tell that something
// was removed, rather than see a truncated or silently altered diagnostic.
const Placeholder = "[redacted credential]"

// minSecret is the shortest value worth treating as a credential. Below it the
// risk inverts: a two-character "key" from a fixture would match ordinary prose
// and shred every artifact it appears in. Real provider keys are 16 characters
// or longer.
const minSecret = 8

var (
	mu      sync.RWMutex
	secrets []string
)

// Register records credential values that must never be persisted, prompted or
// logged. Empty, short and duplicate values are ignored, so callers can hand it
// an unconfigured field without checking first.
func Register(values ...string) {
	mu.Lock()
	defer mu.Unlock()
	for _, v := range values {
		for _, form := range []string{v, url.QueryEscape(v)} {
			form = strings.TrimSpace(form)
			if len(form) < minSecret {
				continue
			}
			known := false
			for _, s := range secrets {
				if s == form {
					known = true
				}
			}
			if !known {
				secrets = append(secrets, form)
			}
		}
	}
}

// String replaces every registered credential in s. It is safe to call on text
// that contains none, and on every write path, however hot.
func String(s string) string {
	mu.RLock()
	defer mu.RUnlock()
	if len(secrets) == 0 || s == "" {
		return s
	}
	for _, secret := range secrets {
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, Placeholder)
		}
	}
	return s
}

// Bytes redacts a serialised artifact in place of its string form. JSON escapes
// nothing in an alphanumeric key, so a credential survives marshalling intact
// and is found by the same literal search.
func Bytes(b []byte) []byte {
	if len(secrets) == 0 || len(b) == 0 {
		return b
	}
	s := String(string(b))
	return []byte(s)
}

// Strings redacts a slice, returning a new one. Diagnostics travel as []string
// through packs, dossiers and metadata.
func Strings(xs []string) []string {
	if len(xs) == 0 {
		return xs
	}
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = String(x)
	}
	return out
}

// Error returns an error whose message carries no credential. A nil error stays
// nil so call sites keep their ordinary shape.
func Error(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	clean := String(msg)
	if clean == msg {
		return err
	}
	return redacted{msg: clean, wrapped: err}
}

// redacted preserves errors.Is/As against the original sentinel while replacing
// the message. A provider failure is still ErrUnavailable after sanitisation.
type redacted struct {
	msg     string
	wrapped error
}

func (r redacted) Error() string { return r.msg }
func (r redacted) Unwrap() error { return r.wrapped }
