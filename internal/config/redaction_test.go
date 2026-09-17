package config

import (
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
