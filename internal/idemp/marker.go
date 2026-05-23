// Package idemp provides loop-break primitives for the bidirectional bridge:
// a stable HTML-comment marker that is appended to every outbound write, and
// a short concurrency-safe LRU of recently-seen event keys. Together they
// prevent the mirror loop that would otherwise occur when a write we made on
// one side bounces back through that side's webhook.
package idemp

import (
	"regexp"
	"strings"
)

// Source identifies which side originated a change.
type Source string

const (
	SourceForge Source = "forge"
	SourcePlane Source = "plane"
)

// Marker is a parsed loop-break marker.
type Marker struct {
	Source  Source
	EventID string
}

// markerRe matches a loop-break marker trailer. The marker must be at the end
// of the body, optionally preceded by up to two blank lines, and optionally
// followed by trailing whitespace (including a missing trailing newline).
//
// The event-ID character set is restricted to ASCII letters, digits, '-', and
// '_' so we never echo back attacker-controlled HTML.
var markerRe = regexp.MustCompile(
	`(?:\r?\n[ \t]*){0,3}<!--[ \t]+pfb:src=(forge|plane),evt=([A-Za-z0-9_-]+)[ \t]+-->[ \t\r\n]*\z`,
)

// validEventID is used to reject event IDs containing characters we won't
// emit, so callers can't accidentally inject HTML through Wrap.
var validEventID = regexp.MustCompile(`\A[A-Za-z0-9_-]+\z`)

// formatMarker renders the canonical marker trailer.
func formatMarker(src Source, eventID string) string {
	return "<!-- pfb:src=" + string(src) + ",evt=" + eventID + " -->"
}

// Wrap appends the marker trailer to body. Idempotent: if body already ends
// in the exact same marker, body is returned unchanged. If body ends in a
// different marker, the new marker is still appended — Wrap tracks by exact
// match, not by presence of any marker.
//
// If src or eventID is invalid (unknown source, empty or non-conforming event
// ID), the body is returned unchanged.
func Wrap(body string, src Source, eventID string) string {
	if src != SourceForge && src != SourcePlane {
		return body
	}
	if !validEventID.MatchString(eventID) {
		return body
	}

	marker := formatMarker(src, eventID)

	// Idempotency: only treat as a no-op if the existing trailing marker is
	// exactly the one we'd append. A trailer with a different (src, evt)
	// gets a second marker appended.
	if existing, ok := Extract(body); ok && existing.Source == src && existing.EventID == eventID {
		return body
	}

	// Preserve a separating blank line so the marker doesn't get glued to a
	// non-empty final line.
	if body == "" {
		return marker
	}
	if strings.HasSuffix(body, "\n") {
		return body + "\n" + marker
	}
	return body + "\n\n" + marker
}

// Extract returns the parsed marker from body if one is present at the end.
// Tolerates trailing whitespace, missing trailing newline, and up to two
// blank lines between the body and the marker.
func Extract(body string) (Marker, bool) {
	m := markerRe.FindStringSubmatch(body)
	if m == nil {
		return Marker{}, false
	}
	return Marker{Source: Source(m[1]), EventID: m[2]}, true
}

// Strip removes the trailing marker (if any) and returns the cleaned body.
// Trailing whitespace and blank lines left behind by removing the marker are
// also trimmed.
func Strip(body string) string {
	loc := markerRe.FindStringIndex(body)
	if loc == nil {
		return body
	}
	return strings.TrimRight(body[:loc[0]], " \t\r\n")
}
