package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

// HeaderSignature is the HTTP header Gitea/Forgejo uses for the body HMAC.
const HeaderSignature = "X-Gitea-Signature"

// HeaderEvent is the HTTP header that names the event kind.
const HeaderEvent = "X-Gitea-Event"

// HeaderDelivery is the HTTP header that carries the unique delivery UUID.
// We use this as the loop-break event ID.
const HeaderDelivery = "X-Gitea-Delivery"

// VerifySignature checks the X-Gitea-Signature header against body using the
// shared secret. The header is the hex-encoded HMAC-SHA256 of the raw request
// body. Comparison is constant time (hmac.Equal).
//
// Returns:
//   - ErrEmptySecret      if secret is "".
//   - ErrMissingSignature if the header is absent or empty.
//   - ErrInvalidSignature if the header value is not valid hex or does not
//     match the computed MAC.
//   - nil                 on success.
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
	if !hmac.Equal(gotMAC, wantMAC) {
		return ErrInvalidSignature
	}
	return nil
}

// computeSignature is a small helper exposed for tests and for any caller
// that needs to sign an outbound replay (e.g. e2e harness). The result is the
// hex-encoded HMAC-SHA256 of body under secret — the exact value Gitea and
// Forgejo place in the X-Gitea-Signature header.
func computeSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
