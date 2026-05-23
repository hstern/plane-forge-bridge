package sync

import (
	"regexp"
	"strconv"
)

// PR ref grammar. Two surfaces in priority order:
//
//  1. Bracket reference like [PFB-42] in the PR title or body. The
//     identifier is matched case-sensitively (Plane's canonical UPPERCASE
//     form), so [pfb-42] does NOT match — that case is reserved for the
//     branch-name surface, where the identifier is conventionally lower.
//  2. Branch prefix like "pfb-42-add-thing" on the head branch ref.
//     Identifier matched case-INSENSITIVELY because branch names follow
//     the lowercase convention; the suffix must be either "-" (continuing
//     the slug) or end-of-string (a branch named exactly "pfb-42").
//
// Both surfaces capture the sequence ID as digits. Anything outside the
// int range is rejected as ok=false — Plane's sequence IDs are 32-bit-ish,
// so 99999999999999999999 in the body is operator garbage, not a target.
//
// We deliberately require the SAME identifier the link is configured for.
// Scanning for arbitrary [XXX-N] references would mis-route events to the
// wrong project; the link's identifier is the source of truth.

// bracketPattern builds the regex matching [<IDENT>-N] anchored on the
// digits. The identifier is escaped to keep regex metacharacters in
// project names from corrupting the pattern (defensive; the loader
// already forbids hyphens, but we shouldn't rely on that here).
func bracketPattern(identifier string) *regexp.Regexp {
	return regexp.MustCompile(`\[` + regexp.QuoteMeta(identifier) + `-([0-9]+)\]`)
}

// branchPattern builds the regex matching "<ident>-N" at the start of a
// branch name. (?i) makes the identifier match case-insensitively; the
// trailing (?:-|$) ensures we don't match "pfb-42abc" as 42 (the digit
// run must be followed by a slug separator or end of string).
func branchPattern(identifier string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(identifier) + `-([0-9]+)(?:-|$)`)
}

// parsePRRef extracts a (projectIdentifier, sequenceID) tuple from a PR's
// title, body, and head branch ref. Sources are tried in order; the first
// match wins:
//
//  1. title bracket reference like [PFB-123] (Plane's syntax) — case-
//     sensitive on the identifier, anchored on the digits.
//  2. body bracket reference (same regex).
//  3. head branch name pattern like "pfb-123-add-thing" — case-INSENSITIVE
//     on the identifier (branch names are lowercase by convention).
//
// Returns ok=false when no usable reference is found. Both patterns
// require the identifier to be the SAME identifier passed in — scanning
// for arbitrary identifiers would link to the wrong project.
//
// If the captured digit run doesn't fit in int (very long literals like
// 99999999999999999999), the source is rejected and parsing falls through
// to the next source. If every source overflows, ok=false.
func parsePRRef(identifier, title, body, headRef string) (sequenceID int, ok bool) {
	bracket := bracketPattern(identifier)

	// Title first.
	if n, found := firstMatch(bracket, title); found {
		return n, true
	}
	// Body second.
	if n, found := firstMatch(bracket, body); found {
		return n, true
	}
	// Head branch third (case-insensitive identifier).
	branch := branchPattern(identifier)
	if n, found := firstMatch(branch, headRef); found {
		return n, true
	}
	return 0, false
}

// firstMatch returns the first regex submatch in s as an int, gating on
// the int range. A capture group that parses cleanly but overflows int is
// treated as no-match so parsePRRef can fall through to the next source —
// e.g. "[PFB-99999999999999999999]" doesn't accidentally short-circuit a
// later valid bracket or branch reference.
func firstMatch(re *regexp.Regexp, s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0, false
	}
	// strconv.Atoi gates on the platform int range and rejects overflow
	// directly. We deliberately don't accept numbers larger than int — the
	// caller's downstream API takes int — so a 21-digit literal in the body
	// falls through to ok=false rather than wrapping to garbage.
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
