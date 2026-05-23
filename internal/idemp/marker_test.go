package idemp

import (
	"strings"
	"testing"
)

func TestWrap_NoExistingMarker(t *testing.T) {
	got := Wrap("Hello world", SourceForge, "01HXYZ123ABC")
	want := "Hello world\n\n<!-- pfb:src=forge,evt=01HXYZ123ABC -->"
	if got != want {
		t.Fatalf("Wrap: got %q want %q", got, want)
	}
}

func TestWrap_EmptyBody(t *testing.T) {
	got := Wrap("", SourcePlane, "abc")
	want := "<!-- pfb:src=plane,evt=abc -->"
	if got != want {
		t.Fatalf("Wrap empty: got %q want %q", got, want)
	}
}

func TestWrap_BodyEndsWithNewline(t *testing.T) {
	got := Wrap("Hello\n", SourceForge, "evt1")
	want := "Hello\n\n<!-- pfb:src=forge,evt=evt1 -->"
	if got != want {
		t.Fatalf("Wrap newline-suffix: got %q want %q", got, want)
	}
}

func TestWrap_Idempotent(t *testing.T) {
	first := Wrap("body text", SourceForge, "01HXYZ123ABC")
	second := Wrap(first, SourceForge, "01HXYZ123ABC")
	if first != second {
		t.Fatalf("Wrap not idempotent:\n first  = %q\n second = %q", first, second)
	}
	// And a third call doesn't add anything either.
	third := Wrap(second, SourceForge, "01HXYZ123ABC")
	if third != second {
		t.Fatalf("Wrap third pass changed body:\n second = %q\n third  = %q", second, third)
	}
	// There must be exactly one marker.
	if n := strings.Count(third, "<!--"); n != 1 {
		t.Fatalf("expected 1 marker, got %d in %q", n, third)
	}
}

func TestWrap_DifferentMarker(t *testing.T) {
	// A body that already carries a different marker should get a second
	// marker appended — we track exact match, not presence.
	pre := "body\n\n<!-- pfb:src=plane,evt=other -->"
	got := Wrap(pre, SourceForge, "evtNew")
	if !strings.HasSuffix(got, "<!-- pfb:src=forge,evt=evtNew -->") {
		t.Fatalf("expected new marker appended, got %q", got)
	}
	if strings.Count(got, "<!--") != 2 {
		t.Fatalf("expected 2 markers, got %d in %q", strings.Count(got, "<!--"), got)
	}
}

func TestWrap_InvalidSourceUnchanged(t *testing.T) {
	in := "hello"
	got := Wrap(in, Source("github"), "evt1")
	if got != in {
		t.Fatalf("invalid source: got %q want %q", got, in)
	}
}

func TestWrap_InvalidEventIDUnchanged(t *testing.T) {
	cases := []string{"", "has space", "<script>", "a/b", "a.b"}
	for _, ev := range cases {
		in := "hello"
		got := Wrap(in, SourceForge, ev)
		if got != in {
			t.Errorf("invalid event id %q: got %q want %q", ev, got, in)
		}
	}
}

func TestExtract_Present(t *testing.T) {
	in := "hello\n\n<!-- pfb:src=forge,evt=evt1 -->"
	m, ok := Extract(in)
	if !ok {
		t.Fatalf("expected marker present")
	}
	if m.Source != SourceForge || m.EventID != "evt1" {
		t.Fatalf("unexpected marker: %+v", m)
	}
}

func TestExtract_Missing(t *testing.T) {
	if _, ok := Extract("hello world"); ok {
		t.Fatalf("expected no marker")
	}
	if _, ok := Extract(""); ok {
		t.Fatalf("expected no marker for empty")
	}
	// Mid-body marker — we only extract from the end.
	if _, ok := Extract("<!-- pfb:src=forge,evt=evt1 -->\nstuff after"); ok {
		t.Fatalf("did not expect to extract mid-body marker")
	}
}

func TestExtract_Whitespace(t *testing.T) {
	cases := []string{
		"hello\n<!-- pfb:src=plane,evt=abc -->",
		"hello\n\n<!-- pfb:src=plane,evt=abc -->",
		"hello\n\n\n<!-- pfb:src=plane,evt=abc -->",
		"hello\n<!-- pfb:src=plane,evt=abc -->\n",
		"hello\n<!-- pfb:src=plane,evt=abc -->\n\n",
		"hello\n<!-- pfb:src=plane,evt=abc -->   ",
		"hello\r\n<!-- pfb:src=plane,evt=abc -->\r\n",
		"hello\r\n\r\n<!-- pfb:src=plane,evt=abc -->",
	}
	for _, c := range cases {
		m, ok := Extract(c)
		if !ok {
			t.Errorf("expected marker present in %q", c)
			continue
		}
		if m.Source != SourcePlane || m.EventID != "abc" {
			t.Errorf("bad parse for %q: %+v", c, m)
		}
	}
}

func TestExtract_RejectsBadEventID(t *testing.T) {
	cases := []string{
		"hello\n<!-- pfb:src=forge,evt=<script>alert(1)</script> -->",
		"hello\n<!-- pfb:src=forge,evt=has space -->",
		"hello\n<!-- pfb:src=forge,evt=a.b -->",
		"hello\n<!-- pfb:src=forge,evt= -->",
		"hello\n<!-- pfb:src=forge,evt=ok -->extra",
		"hello\n<!--pfb:src=forge,evt=ok-->", // missing required spaces
		"hello\n<!-- pfb:src=github,evt=ok -->",
	}
	for _, c := range cases {
		if m, ok := Extract(c); ok {
			t.Errorf("expected reject for %q, got %+v", c, m)
		}
	}
}

func TestStrip_PresentAbsent(t *testing.T) {
	// Absent: unchanged.
	if got := Strip("hello world"); got != "hello world" {
		t.Errorf("strip no-marker: got %q", got)
	}

	// Present: content preserved, trailing blank lines trimmed.
	in := "hello world\n\n<!-- pfb:src=forge,evt=evt1 -->\n"
	if got := Strip(in); got != "hello world" {
		t.Errorf("strip with marker: got %q", got)
	}

	// Wrap then Strip round-trip preserves the body.
	body := "first line\nsecond line"
	if got := Strip(Wrap(body, SourceForge, "evt1")); got != body {
		t.Errorf("round trip: got %q want %q", got, body)
	}

	// Only-marker body strips to empty.
	if got := Strip("<!-- pfb:src=plane,evt=x -->"); got != "" {
		t.Errorf("only-marker strip: got %q", got)
	}
}
