package forge

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Parse decodes a verified webhook delivery into an Event.
//
// The caller MUST call VerifySignature first — Parse does not check the HMAC.
// Use VerifyAndParse if you want both in one call.
//
// Returns ErrMissingEventHeader if X-Gitea-Event is absent,
// ErrUnsupportedEvent if it names an event we do not handle, and a wrapped
// ErrMalformedPayload if the body is not valid JSON for the declared event.
func Parse(headers http.Header, body []byte) (*Event, error) {
	eventName := headers.Get(HeaderEvent)
	if eventName == "" {
		return nil, ErrMissingEventHeader
	}
	delivery := headers.Get(HeaderDelivery)

	switch eventName {
	case "issues":
		return parseIssue(delivery, body)
	case "issue_comment":
		return parseIssueComment(delivery, body)
	case "pull_request":
		return parsePullRequest(delivery, body)
	case "pull_request_review":
		return parsePullRequestReview(delivery, body)
	case "push":
		return parsePush(delivery, body)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedEvent, eventName)
	}
}

// VerifyAndParse is the convenience wrapper most HTTP handlers want: it
// checks the signature, then parses the body. Errors propagate unchanged so
// callers can errors.Is the sentinels.
func VerifyAndParse(secret string, headers http.Header, body []byte) (*Event, error) {
	if err := VerifySignature(secret, headers, body); err != nil {
		return nil, err
	}
	return Parse(headers, body)
}

// decodePayload is the shared JSON decoder. We do NOT enable
// DisallowUnknownFields because forges add fields over time and we want
// forward compatibility.
func decodePayload(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%w: %w", ErrMalformedPayload, err)
	}
	return nil
}

// issuePayload is the wire shape of an "issues" delivery.
type issuePayload struct {
	Action     string     `json:"action"`
	Number     int64      `json:"number"`
	Issue      *Issue     `json:"issue"`
	Repository Repository `json:"repository"`
	Sender     User       `json:"sender"`
}

func parseIssue(delivery string, body []byte) (*Event, error) {
	var p issuePayload
	if err := decodePayload(body, &p); err != nil {
		return nil, err
	}
	kind, ok := issueActionToKind(p.Action)
	if !ok {
		return nil, fmt.Errorf("%w: issues action %q", ErrUnsupportedEvent, p.Action)
	}
	if p.Issue == nil {
		return nil, fmt.Errorf("%w: issues payload missing issue object", ErrMalformedPayload)
	}
	return &Event{
		Kind:       kind,
		DeliveryID: delivery,
		Repo:       p.Repository,
		Sender:     p.Sender,
		Issue:      p.Issue,
		Raw:        body,
	}, nil
}

func issueActionToKind(action string) (EventKind, bool) {
	switch action {
	case "opened":
		return EventIssueOpened, true
	case "edited":
		return EventIssueEdited, true
	case "closed":
		return EventIssueClosed, true
	case "reopened":
		return EventIssueReopened, true
	case "label_updated", "labeled":
		return EventIssueLabeled, true
	case "label_cleared", "unlabeled":
		return EventIssueUnlabeled, true
	case "assigned", "unassigned":
		return EventIssueAssigned, true
	default:
		return "", false
	}
}

// issueCommentPayload is the wire shape of an "issue_comment" delivery.
type issueCommentPayload struct {
	Action     string     `json:"action"`
	Issue      *Issue     `json:"issue"`
	Comment    *Comment   `json:"comment"`
	Repository Repository `json:"repository"`
	Sender     User       `json:"sender"`
}

func parseIssueComment(delivery string, body []byte) (*Event, error) {
	var p issueCommentPayload
	if err := decodePayload(body, &p); err != nil {
		return nil, err
	}
	var kind EventKind
	switch p.Action {
	case "created":
		kind = EventIssueCommentCreated
	case "edited":
		kind = EventIssueCommentEdited
	case "deleted":
		kind = EventIssueCommentDeleted
	default:
		return nil, fmt.Errorf("%w: issue_comment action %q", ErrUnsupportedEvent, p.Action)
	}
	if p.Comment == nil {
		return nil, fmt.Errorf("%w: issue_comment payload missing comment object", ErrMalformedPayload)
	}
	return &Event{
		Kind:       kind,
		DeliveryID: delivery,
		Repo:       p.Repository,
		Sender:     p.Sender,
		Issue:      p.Issue,
		Comment:    p.Comment,
		Raw:        body,
	}, nil
}

// pullRequestPayload is the wire shape of a "pull_request" delivery.
type pullRequestPayload struct {
	Action      string       `json:"action"`
	Number      int64        `json:"number"`
	PullRequest *PullRequest `json:"pull_request"`
	Repository  Repository   `json:"repository"`
	Sender      User         `json:"sender"`
}

func parsePullRequest(delivery string, body []byte) (*Event, error) {
	var p pullRequestPayload
	if err := decodePayload(body, &p); err != nil {
		return nil, err
	}
	var kind EventKind
	switch p.Action {
	case "opened":
		kind = EventPullRequestOpened
	case "edited":
		kind = EventPullRequestEdited
	case "closed":
		// The merged flag inside the PR object tells us whether this was a
		// merge or a plain close; both flow through EventPullRequestClosed
		// and callers check evt.PullRequest.Merged.
		kind = EventPullRequestClosed
	case "reopened":
		kind = EventPullRequestReopened
	default:
		return nil, fmt.Errorf("%w: pull_request action %q", ErrUnsupportedEvent, p.Action)
	}
	if p.PullRequest == nil {
		return nil, fmt.Errorf("%w: pull_request payload missing pull_request object", ErrMalformedPayload)
	}
	return &Event{
		Kind:        kind,
		DeliveryID:  delivery,
		Repo:        p.Repository,
		Sender:      p.Sender,
		PullRequest: p.PullRequest,
		Raw:         body,
	}, nil
}

// pullRequestReviewPayload is the wire shape of a "pull_request_review"
// delivery. We collapse all review actions into a single EventKind and let
// callers branch on Review.Type if they care.
type pullRequestReviewPayload struct {
	Action      string       `json:"action"`
	PullRequest *PullRequest `json:"pull_request"`
	Review      *Review      `json:"review"`
	Repository  Repository   `json:"repository"`
	Sender      User         `json:"sender"`
}

func parsePullRequestReview(delivery string, body []byte) (*Event, error) {
	var p pullRequestReviewPayload
	if err := decodePayload(body, &p); err != nil {
		return nil, err
	}
	if p.Review == nil {
		return nil, fmt.Errorf("%w: pull_request_review payload missing review object", ErrMalformedPayload)
	}
	return &Event{
		Kind:        EventPullRequestReview,
		DeliveryID:  delivery,
		Repo:        p.Repository,
		Sender:      p.Sender,
		PullRequest: p.PullRequest,
		Review:      p.Review,
		Raw:         body,
	}, nil
}

// pushPayload is the wire shape of a "push" delivery.
type pushPayload struct {
	Ref        string       `json:"ref"`
	Before     string       `json:"before"`
	After      string       `json:"after"`
	CompareURL string       `json:"compare_url"`
	Commits    []PushCommit `json:"commits"`
	Pusher     User         `json:"pusher"`
	Repository Repository   `json:"repository"`
	Sender     User         `json:"sender"`
}

func parsePush(delivery string, body []byte) (*Event, error) {
	var p pushPayload
	if err := decodePayload(body, &p); err != nil {
		return nil, err
	}
	return &Event{
		Kind:       EventPush,
		DeliveryID: delivery,
		Repo:       p.Repository,
		Sender:     p.Sender,
		Push: &Push{
			Ref:        p.Ref,
			Before:     p.Before,
			After:      p.After,
			CompareURL: p.CompareURL,
			Commits:    p.Commits,
			Pusher:     p.Pusher,
		},
		Raw: body,
	}, nil
}
