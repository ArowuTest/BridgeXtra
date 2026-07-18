package configsvc

// EXT-4: one strict decode path for ALL governed config content. Plain
// json.Unmarshal silently DROPS unknown fields, so a typo — `fee_bp` for
// `fee_bps`, `max_attempt` for `max_attempts` — is accepted and the intended
// value silently defaults to zero, arming a wrong policy. strictUnmarshal
// refuses anything the target struct does not fully model: unknown fields,
// trailing data after the document, and oversize content. Every domain
// validator decodes through here, so validation can only pass content the
// platform completely understands (no-hardcoding's sibling: no silent-ignore).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// maxConfigContentBytes bounds a single governed config document. These are
// small policy records; anything larger is malformed or hostile.
const maxConfigContentBytes = 1 << 16 // 64 KiB

func strictUnmarshal(content json.RawMessage, v any) error {
	if len(content) > maxConfigContentBytes {
		return fmt.Errorf("config content exceeds %d bytes", maxConfigContentBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields() // typo'd / unmodelled field => hard error
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("config content: %w", err)
	}
	// Exactly one JSON document — reject trailing data (the decoder's default
	// nesting limit bounds depth).
	if err := dec.Decode(&json.RawMessage{}); err != io.EOF {
		return fmt.Errorf("config content: unexpected trailing data after JSON document")
	}
	return nil
}
