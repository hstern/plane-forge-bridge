package sync

import "testing"

func TestParsePRRef_BracketTitle(t *testing.T) {
	t.Parallel()
	n, ok := parsePRRef("PFB", "[PFB-42] fix login", "", "")
	if !ok || n != 42 {
		t.Fatalf("got (%d,%v), want (42,true)", n, ok)
	}
}

func TestParsePRRef_BracketBody(t *testing.T) {
	t.Parallel()
	n, ok := parsePRRef("PFB", "fix login", "Closes [PFB-42].", "")
	if !ok || n != 42 {
		t.Fatalf("got (%d,%v), want (42,true)", n, ok)
	}
}

func TestParsePRRef_BracketWrongIdentifier(t *testing.T) {
	t.Parallel()
	// [OTHER-42] must NOT match for identifier=PFB — scanning arbitrary
	// identifiers would link to the wrong project.
	n, ok := parsePRRef("PFB", "[OTHER-42] fix login", "Closes [OTHER-42].", "")
	if ok {
		t.Fatalf("matched wrong identifier: got %d", n)
	}
}

func TestParsePRRef_BranchPrefix(t *testing.T) {
	t.Parallel()
	n, ok := parsePRRef("PFB", "fix login", "", "pfb-42-add-thing")
	if !ok || n != 42 {
		t.Fatalf("got (%d,%v), want (42,true)", n, ok)
	}
}

func TestParsePRRef_BranchPrefixCaseInsensitive(t *testing.T) {
	t.Parallel()
	n, ok := parsePRRef("PFB", "fix login", "", "PFB-42-foo")
	if !ok || n != 42 {
		t.Fatalf("got (%d,%v), want (42,true)", n, ok)
	}
}

func TestParsePRRef_BranchExactNumberNoSuffix(t *testing.T) {
	t.Parallel()
	// "pfb-42" with no slug suffix is still a valid match — the regex
	// alternation accepts "-" OR end of string after the digit run.
	n, ok := parsePRRef("PFB", "", "", "pfb-42")
	if !ok || n != 42 {
		t.Fatalf("got (%d,%v), want (42,true)", n, ok)
	}
}

func TestParsePRRef_BranchNoMatch(t *testing.T) {
	t.Parallel()
	_, ok := parsePRRef("PFB", "fix login", "", "feature/something")
	if ok {
		t.Fatal("expected no match on unrelated branch")
	}
}

func TestParsePRRef_BranchDigitsRunOn_NoMatch(t *testing.T) {
	t.Parallel()
	// "pfb-42abc" must NOT match as 42 — the digit run must be followed
	// by a slug separator or end of string. Otherwise "pfb-42abc" would
	// route to sequence 42 by accident.
	_, ok := parsePRRef("PFB", "", "", "pfb-42abc")
	if ok {
		t.Fatal("expected no match when digits aren't followed by '-' or end")
	}
}

func TestParsePRRef_TitleWinsOverBranch(t *testing.T) {
	t.Parallel()
	// Title 7 should win even though the branch carries 99.
	n, ok := parsePRRef("PFB", "[PFB-7] tiny fix", "", "pfb-99-bigger-fix")
	if !ok || n != 7 {
		t.Fatalf("got (%d,%v), want (7,true) — title must win over branch", n, ok)
	}
}

func TestParsePRRef_BodyWinsOverBranch(t *testing.T) {
	t.Parallel()
	n, ok := parsePRRef("PFB", "fix login", "Closes [PFB-7].", "pfb-99-bigger-fix")
	if !ok || n != 7 {
		t.Fatalf("got (%d,%v), want (7,true) — body must win over branch", n, ok)
	}
}

func TestParsePRRef_NumberTooLarge(t *testing.T) {
	t.Parallel()
	// 21-digit literal overflows int on every platform we target. Falls
	// through to ok=false rather than wrapping or silently truncating.
	_, ok := parsePRRef("PFB", "[PFB-99999999999999999999]", "", "")
	if ok {
		t.Fatal("expected no match for overflowing sequence number")
	}
}

func TestParsePRRef_MultipleInBody(t *testing.T) {
	t.Parallel()
	// First match wins. Body cites PFB-3 first, PFB-9 second; we return 3.
	n, ok := parsePRRef("PFB", "", "Refs [PFB-3] and [PFB-9].", "")
	if !ok || n != 3 {
		t.Fatalf("got (%d,%v), want (3,true) — first match in body wins", n, ok)
	}
}

func TestParsePRRef_ZeroRejected(t *testing.T) {
	t.Parallel()
	// Sequence IDs are positive integers; 0 is operator garbage.
	_, ok := parsePRRef("PFB", "[PFB-0] nope", "", "")
	if ok {
		t.Fatal("expected no match on sequence 0")
	}
}

func TestParsePRRef_EmptyEverything(t *testing.T) {
	t.Parallel()
	_, ok := parsePRRef("PFB", "", "", "")
	if ok {
		t.Fatal("expected no match on empty sources")
	}
}
