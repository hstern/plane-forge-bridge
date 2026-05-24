package plane

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

// VerifySignature checks the X-Plane-Signature header against an HMAC-SHA256
// of body, keyed by secret, hex-encoded. The comparison is constant-time.
//
// Plane's webhook task computes:
//
//	hmac.new(secret, json.dumps(payload), hashlib.sha256).hexdigest()
//
// (apps/api/plane/bgtasks/webhook_task.py). The body passed here MUST be the
// exact raw request body — re-serializing the JSON will reorder keys and
// break verification.
//
// Empty secret is rejected with ErrEmptySecret rather than computing an
// HMAC over an empty key (which would always succeed against an attacker
// who knows the body). Mirrors internal/forge.VerifySignature.
//
// The supplied header is hex-decoded first; a malformed-hex header
// surfaces as ErrInvalidSignature distinct from "valid hex, wrong MAC".
// The hmac.Equal compare is then on the raw MAC bytes, which is the
// canonical pattern.
func VerifySignature(secret string, headers http.Header, body []byte) error {
	if secret == "" {
		return ErrEmptySecret
	}
	got := headers.Get(HeaderSignature)
	if got == "" {
		return ErrMissingSignature
	}
	gotMAC, err := hex.DecodeString(got)
	if err != nil {
		return ErrInvalidSignature
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	wantMAC := mac.Sum(nil)

	if !hmac.Equal(wantMAC, gotMAC) {
		return ErrInvalidSignature
	}
	return nil
}
