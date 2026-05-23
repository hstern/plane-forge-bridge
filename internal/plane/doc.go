// Package plane parses and verifies webhook deliveries from a Plane instance
// (https://plane.so) and provides the minimal type surface needed by the
// bridge to translate work-item and comment events into forge events.
//
// # Webhook contract (as of 2026-05; verified against makeplane/plane main)
//
// Plane sends three headers with every webhook delivery:
//
//   - X-Plane-Delivery   unique UUID per delivery, used as the loop-break
//     event id.
//   - X-Plane-Event      event type ("project", "issue", "cycle", "module",
//     "issue_comment", ...).
//   - X-Plane-Signature  HMAC-SHA256 of the raw request body, keyed by the
//     webhook secret, hex-encoded with no prefix.
//
// The body is JSON with the envelope:
//
//	{
//	  "event":        "<event-type>",
//	  "action":       "create" | "update" | "delete",
//	  "webhook_id":   "<uuid>",
//	  "workspace_id": "<uuid>",
//	  "data":         { ... entity payload ... },
//	  "activity":     { "actor": { "id": "<uuid>", "display_name": "..." } }
//	}
//
// For delete events the data object contains only the id of the deleted
// entity.
//
// Sources consulted:
//   - https://developers.plane.so/dev-tools/intro-webhooks
//   - https://developers.plane.so/dev-tools/build-plane-app/webhooks
//   - makeplane/plane: apps/api/plane/bgtasks/webhook_task.py
//     (signature generation, header names, payload envelope)
//   - makeplane/plane: apps/api/plane/api/serializers/issue.py
//     (IssueExpandSerializer, IssueCommentSerializer field layout)
//   - makeplane/plane: apps/api/plane/db/models/issue.py
//     (Issue + IssueComment model fields)
//
// Assumptions flagged for a future agent to verify against a live Plane
// instance:
//
//   - Signature is the hex digest of HMAC-SHA256(secret, raw_body) with no
//     "sha256=" prefix. Confirmed by webhook_task.py: hmac_signature.hexdigest().
//   - Plane does not currently emit comment.deleted via a dedicated
//     issue_comment delete event in the OSS source; the SERIALIZER_MAPPER
//     handles issue_comment uniformly, so we model the kind but the live
//     payload shape for delete should be verified.
package plane
