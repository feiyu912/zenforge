// Package redact provides a secret-bearing string whose every formatting
// path redacts its value, modelled on codex's RedactedString.
//
// Serialization stays transparent so a configuration file round-trips
// unchanged; everything that lands in a log line, an error message, or a
// panic trace redacts.
package redact

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// Placeholder replaces a redacted value in formatted output.
const Placeholder = "<redacted>"

// String is a secret string.
//
// Use Reveal to read the value. String, GoString, and LogValue all
// return Placeholder, so `%v`, `%s`, `%#v`, and slog output never leak
// the secret. MarshalJSON is intentionally transparent: a config file
// that was loaded must be writable again byte-for-byte.
type String string

// New wraps a secret.
func New(value string) String { return String(value) }

// Reveal returns the underlying secret. Callers should keep the result
// out of logs and event payloads.
func (s String) Reveal() string { return string(s) }

// IsZero reports whether the secret is unset.
func (s String) IsZero() bool { return s == "" }

func (s String) String() string { return Placeholder }

// GoString covers the `%#v` verb.
func (s String) GoString() string { return fmt.Sprintf("redact.String(%q)", Placeholder) }

// LogValue covers structured logging.
func (s String) LogValue() slog.Value { return slog.StringValue(Placeholder) }

// MarshalJSON writes the real value so configuration round-trips.
func (s String) MarshalJSON() ([]byte, error) { return json.Marshal(string(s)) }

// UnmarshalJSON accepts a JSON string or null.
func (s *String) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("redact: UnmarshalJSON on nil *String")
	}
	if string(data) == "null" {
		*s = ""
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = String(value)
	return nil
}

// Describe renders a secret for diagnostics: set secrets report only
// whether they are present, never their length or content.
func (s String) Describe() string {
	if s.IsZero() {
		return "unset"
	}
	return "set"
}
