package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/redact"
)

// credentialField reports whether a settings field holds a secret. Registration
// is the half that makes every redacting sink work: a key that is configured
// but never registered is redacted nowhere.
func credentialField(name string) bool {
	return strings.Contains(name, "Key") || strings.Contains(name, "Secret")
}

// The walk is deliberately reflective rather than a written-out list. A new
// provider adds a field to Settings, and the failure mode this guards against
// is exactly that nobody remembers to add it to registerSecrets as well.
func TestEveryCredentialFieldInSettingsIsRegistered(t *testing.T) {
	var s Settings
	values := map[string]string{}
	var fill func(v reflect.Value, path string)
	fill = func(v reflect.Value, path string) {
		if v.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			f, name := v.Field(i), v.Type().Field(i).Name
			if !f.CanSet() {
				continue
			}
			switch f.Kind() {
			case reflect.Struct:
				fill(f, path+name+".")
			case reflect.String:
				if credentialField(name) {
					value := "SECRET" + strings.ToUpper(name) + "0123456789"
					f.SetString(value)
					values[path+name] = value
				}
			}
		}
	}
	fill(reflect.ValueOf(&s).Elem(), "")
	if len(values) == 0 {
		t.Fatal("no credential fields found; the walk stopped matching")
	}
	s.registerSecrets()
	for field, value := range values {
		if got := redact.String("configured=" + value); strings.Contains(got, value) {
			t.Errorf("registerSecrets does not cover %s", field)
		}
	}
}

// TestChiefAPIKeyIsRedactedAndNeverInherited guards the two traps around the
// dedicated Chief API credentials: (1) [chief_api] must never fall back to
// [api]'s key when its own is absent — that must be a loud configuration
// error naming the missing field, not a silent inheritance of the cheap-role
// key; and (2) registerSecrets runs at the very end of Load, after every
// validation return, so a validation error raised before it can never rely on
// redaction to keep a key out of its message — the error text itself must
// never interpolate a credential value.
func TestChiefAPIKeyIsRedactedAndNeverInherited(t *testing.T) {
	t.Run("never inherited when both are configured", func(t *testing.T) {
		_, cwd := isolate(t)
		toml := "chief_engine=\"api\"\n" +
			"[api]\nbase_url=\"https://cheap.invalid/v1\"\nmodel=\"cheap-model\"\napi_key=\"sk-cheap-only\"\n" +
			"[chief_api]\nbase_url=\"https://chief.invalid/v1\"\nmodel=\"chief-model\"\napi_key=\"sk-chief-only\"\n"
		if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s.ChiefAPI.APIKey != "sk-chief-only" {
			t.Errorf("ChiefAPI.APIKey = %q, want its own dedicated key", s.ChiefAPI.APIKey)
		}
		if s.API.APIKey != "sk-cheap-only" {
			t.Errorf("API.APIKey = %q, want the cheap-role key untouched", s.API.APIKey)
		}
	})

	t.Run("missing chief_api.api_key errors loudly without leaking either key", func(t *testing.T) {
		_, cwd := isolate(t)
		const cheapKey = "sk-cheap-must-not-leak"
		toml := "chief_engine=\"api\"\n" +
			"[api]\nbase_url=\"https://cheap.invalid/v1\"\nmodel=\"cheap-model\"\napi_key=\"" + cheapKey + "\"\n" +
			"[chief_api]\nbase_url=\"https://chief.invalid/v1\"\nmodel=\"chief-model\"\n"
		if err := os.WriteFile(filepath.Join(cwd, "cfr.toml"), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := Load()
		if err == nil {
			t.Fatalf("Load succeeded with s=%+v, want an error: chief_api.api_key must not silently inherit [api]'s key", s)
		}
		if !strings.Contains(err.Error(), "chief_api.api_key") {
			t.Errorf("error = %q, want it to name chief_api.api_key", err.Error())
		}
		if strings.Contains(err.Error(), cheapKey) {
			t.Errorf("error %q leaks a configured key value — registerSecrets runs after validation, so the message itself must never interpolate one", err.Error())
		}
	})
}
