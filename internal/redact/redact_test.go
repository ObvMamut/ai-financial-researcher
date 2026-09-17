package redact

import (
	"errors"
	"strings"
	"testing"
)

var errProvider = errors.New("provider unavailable")

func TestRegisteredCredentialsCannotSurviveAnySink(t *testing.T) {
	const key = "AV7QK2ZP4M1XN0BC"
	Register(key)
	// The shape AlphaVantage actually returns: its own request quoted back.
	body := "Invalid API call. Visit https://www.alphavantage.co/query?function=NEWS_SENTIMENT&apikey=" + key + "&tickers=AAA"
	for name, got := range map[string]string{
		"String":  String(body),
		"Bytes":   string(Bytes([]byte(body))),
		"Strings": strings.Join(Strings([]string{"AlphaVantage news/AAA: " + body}), " "),
		"Error":   Error(errors.Join(errProvider, errors.New(body))).Error(),
	} {
		if strings.Contains(got, key) {
			t.Errorf("%s leaked the credential: %s", name, got)
		}
		if !strings.Contains(got, Placeholder) {
			t.Errorf("%s dropped the diagnostic instead of redacting it: %s", name, got)
		}
	}
	if !strings.Contains(String(body), "Invalid API call") {
		t.Error("redaction destroyed the audit trail")
	}
}

func TestRedactedErrorStaysComparable(t *testing.T) {
	const key = "sk-4f2a9c11e7b3d8a6f0"
	Register(key)
	err := Error(errors.Join(errProvider, errors.New("Bearer "+key)))
	if !errors.Is(err, errProvider) {
		t.Fatal("redaction broke errors.Is against the provider sentinel")
	}
	if Error(nil) != nil {
		t.Fatal("nil error must stay nil")
	}
}

func TestShortAndEmptyValuesAreNotTreatedAsCredentials(t *testing.T) {
	// A fixture key of "test" would otherwise shred every artifact mentioning
	// it. Below minSecret the risk inverts, so those registrations are ignored.
	Register("", "   ", "test", "fixture")
	for _, s := range []string{"test", "fixture", "a test fixture run"} {
		if String(s) != s {
			t.Errorf("short value %q was treated as a credential: %s", s, String(s))
		}
	}
}

func TestURLEncodedFormIsAlsoRedacted(t *testing.T) {
	const key = "ab+cd/ef=gh ij"
	Register(key)
	encoded := "https://example.test/query?apikey=ab%2Bcd%2Fef%3Dgh+ij"
	if got := String(encoded); strings.Contains(got, "ab%2Bcd") {
		t.Errorf("percent-encoded credential survived: %s", got)
	}
}
