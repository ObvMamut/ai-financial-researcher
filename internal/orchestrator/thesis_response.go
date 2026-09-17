package orchestrator

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// measureResponse measures a complete model response on both axes a capacity
// budget can be judged by: the raw bytes of the whole response, and the
// normalized size of the fenced JSON payload it actually carries.
//
// It locates the payload with extractLastJSON — the same in-package wrapper
// around parse.LastJSONBlock that decodeResearch and parseIdeas already use
// to parse this same text — so the budget and the parser can never diverge.
// It never decodes into a typed value: json.Compact runs over the exact
// extracted bytes, so unknown fields, exact string contents, numeric
// spellings and quotation bytes survive the measurement byte-identical. A
// typed round-trip (decode into model.CandidateDossier, re-marshal) is not
// lossless — it can renumber, reorder or drop fields the schema does not yet
// know about — so it must never be used here.
//
// When there is no fenced JSON object (extractLastJSON returns false), or the
// extracted bytes fail to compact (malformed JSON — e.g. an unescaped raw
// newline inside a string), Normalized is false and PayloadBytes falls back
// to RawBytes: that fallback is a GATE value, and an unfenced or malformed
// response must not gain a capacity pass by appearing smaller than it is.
//
// The hash and method fields are provenance, not a gate, and get no such
// fallback: when Normalized is false there is no payload to name a method or
// a hash for, so Method and PayloadSHA256 stay "" (unrecorded), never a
// fabricated claim that the payload equals the raw response. RawSHA256 is
// always set — the complete response is always known, whether or not a
// payload could be extracted from it.
func measureResponse(stdout string) model.ResponseMeasure {
	m := model.ResponseMeasure{
		RawBytes:     len(stdout),
		PayloadBytes: len(stdout), // gate fallback; see doc comment above
		RawSHA256:    fmt.Sprintf("%x", sha256.Sum256([]byte(stdout))),
	}

	payload, ok := extractLastJSON(stdout)
	if !ok {
		return m
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(payload)); err != nil {
		return m
	}
	m.Normalized = true
	m.Method = "json.Compact of the fenced payload"
	m.PayloadBytes = buf.Len()
	m.PayloadSHA256 = fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
	return m
}
