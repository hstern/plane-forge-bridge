// Package forge implements webhook signature verification and payload parsing
// for Gitea-API-compatible forges (Forgejo and Gitea).
//
// Forgejo and Gitea share the v1 webhook contract:
//
//   - X-Gitea-Signature: HMAC-SHA256 of the raw body, hex-encoded.
//   - X-Gitea-Event:     event name (e.g. "issues", "issue_comment",
//     "pull_request", "pull_request_review", "push").
//   - X-Gitea-Delivery:  unique delivery UUID; used as the event ID for the
//     loop-break marker.
//
// A single package handles both dialects because the byte-level contract is
// identical for the events the bridge cares about.
//
// Typical usage from an HTTP handler:
//
//	evt, err := forge.VerifyAndParse(secret, r.Header, body)
//	switch {
//	case errors.Is(err, forge.ErrUnsupportedEvent):
//	    // ack with 204 — we don't care about this event
//	case err != nil:
//	    // 401 for signature errors, 400 for malformed
//	default:
//	    // dispatch evt.Kind
//	}
package forge
