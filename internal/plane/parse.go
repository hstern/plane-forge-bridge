package plane

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// envelope mirrors the outer JSON shape Plane sends. The inner data and
// activity are decoded lazily so we can branch on event type.
type envelope struct {
	Event       string          `json:"event"`
	Action      string          `json:"action"`
	WebhookID   string          `json:"webhook_id"`
	WorkspaceID string          `json:"workspace_id"`
	Data        json.RawMessage `json:"data"`
	Activity    *activity       `json:"activity"`
}

type activity struct {
	Actor Actor `json:"actor"`
}

// Parse converts a verified Plane webhook delivery into a typed Event.
//
// It only inspects headers and the body; it does NOT verify the HMAC —
// callers should call VerifySignature first or use VerifyAndParse.
func Parse(headers http.Header, body []byte) (*Event, error) {
	eventHeader := headers.Get(HeaderEvent)
	if eventHeader == "" {
		return nil, ErrMissingEventHeader
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedPayload, err)
	}

	// The header should match the envelope; trust the header (it is set by
	// Plane itself and is what dispatchers usually key on) but fall back to
	// the envelope if the header is unrecognised. This matches the
	// belt-and-braces posture in internal/forge.
	eventType := eventHeader
	if eventType == "" {
		eventType = env.Event
	}

	ev := &Event{
		DeliveryID:  headers.Get(HeaderDelivery),
		Action:      env.Action,
		WorkspaceID: env.WorkspaceID,
		WebhookID:   env.WebhookID,
		Raw:         body,
	}
	if env.Activity != nil {
		ev.Actor = env.Activity.Actor
	}

	switch eventType {
	case planeEventIssue:
		kind, err := workItemKind(env.Action)
		if err != nil {
			return nil, err
		}
		ev.Kind = kind
		wi, err := decodeWorkItem(env.Data)
		if err != nil {
			return nil, err
		}
		ev.WorkItem = wi
	case planeEventIssueComment:
		kind, err := commentKind(env.Action)
		if err != nil {
			return nil, err
		}
		ev.Kind = kind
		c, err := decodeComment(env.Data)
		if err != nil {
			return nil, err
		}
		ev.Comment = c
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedEvent, eventType)
	}

	return ev, nil
}

// VerifyAndParse runs VerifySignature followed by Parse. Both layers are
// independent so callers that need finer-grained error handling (e.g.
// metrics on signature failures specifically) can still call them
// separately.
func VerifyAndParse(secret string, headers http.Header, body []byte) (*Event, error) {
	if err := VerifySignature(secret, headers, body); err != nil {
		return nil, err
	}
	return Parse(headers, body)
}

func workItemKind(action string) (EventKind, error) {
	switch action {
	case actionCreate, actionCreated:
		return EventWorkItemCreated, nil
	case actionUpdate, actionUpdated:
		return EventWorkItemUpdated, nil
	case actionDelete, actionDeleted:
		return EventWorkItemDeleted, nil
	default:
		return "", fmt.Errorf("%w: issue action %q", ErrUnsupportedEvent, action)
	}
}

func commentKind(action string) (EventKind, error) {
	switch action {
	case actionCreate, actionCreated:
		return EventCommentCreated, nil
	case actionUpdate, actionUpdated:
		return EventCommentUpdated, nil
	case actionDelete, actionDeleted:
		return EventCommentDeleted, nil
	default:
		return "", fmt.Errorf("%w: issue_comment action %q", ErrUnsupportedEvent, action)
	}
}

func decodeWorkItem(raw json.RawMessage) (*WorkItem, error) {
	if len(raw) == 0 || string(raw) == "null" {
		// Delete events may omit data entirely; accept a nil work item rather
		// than reject the event.
		return nil, nil
	}
	var wi WorkItem
	if err := json.Unmarshal(raw, &wi); err != nil {
		return nil, fmt.Errorf("%w: work item: %w", ErrMalformedPayload, err)
	}
	return &wi, nil
}

func decodeComment(raw json.RawMessage) (*Comment, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var c Comment
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%w: comment: %w", ErrMalformedPayload, err)
	}
	return &c, nil
}
