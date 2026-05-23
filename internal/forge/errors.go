package forge

import "errors"

// Sentinel errors returned by VerifySignature, Parse and VerifyAndParse.
// Callers can match these with errors.Is to choose an HTTP status code.
var (
	// ErrMissingSignature is returned when the X-Gitea-Signature header is
	// absent or empty. Map to HTTP 401.
	ErrMissingSignature = errors.New("forge: missing X-Gitea-Signature header")

	// ErrInvalidSignature is returned when the signature does not match the
	// body under the configured secret. Map to HTTP 401.
	ErrInvalidSignature = errors.New("forge: invalid X-Gitea-Signature")

	// ErrMissingEventHeader is returned when the X-Gitea-Event header is
	// absent or empty. Map to HTTP 400.
	ErrMissingEventHeader = errors.New("forge: missing X-Gitea-Event header")

	// ErrUnsupportedEvent is returned when the X-Gitea-Event value is not
	// one this package handles. Callers typically ack-and-drop with 204.
	ErrUnsupportedEvent = errors.New("forge: unsupported event type")

	// ErrMalformedPayload wraps a JSON decode error. Map to HTTP 400.
	ErrMalformedPayload = errors.New("forge: malformed payload")

	// ErrEmptySecret is returned by VerifySignature when secret is empty.
	// Refusing to verify against an empty secret is a defence against
	// accidentally accepting any signature when the secret is misconfigured.
	ErrEmptySecret = errors.New("forge: empty webhook secret")
)
