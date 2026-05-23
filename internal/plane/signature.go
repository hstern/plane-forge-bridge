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
func VerifySignature(secret string, headers http.Header, body []byte) error {
	got := headers.Get(HeaderSignature)
	if got == "" {
		return ErrMissingSignature
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))

	// hmac.Equal is constant-time; it also handles length differences safely.
	if !hmac.Equal([]byte(want), []byte(got)) {
		return ErrInvalidSignature
	}
	return nil
}
