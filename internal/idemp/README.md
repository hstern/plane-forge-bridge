# internal/idemp

Loop-break primitives for the bidirectional Plane ↔ Forgejo/Gitea bridge.

## Purpose

`plane-forge-bridge` is a mirror: events arriving on one side cause writes on
the other. Without a defense, every write triggers a webhook that triggers a
write, and the bridge gets stuck in an infinite loop. This package supplies
the two defenses required by `AGENTS.md`:

1. A stable HTML-comment **marker** appended to every outbound write,
   identifying which side and which inbound event caused the write. On
   inbound, if we recognise our own marker we drop the event.
2. A short, in-memory **LRU** of recently-seen `(source, event-id,
   target-object-id)` triples. Belt-and-braces in case a downstream client
   strips the HTML comment from a body before we see it again.

Both layers are kept — they cover different failure modes. The marker is the
durable defense; the LRU is the optimisation/safety net for the case where
the marker has been mangled in transit.

## Marker contract

The marker is the *last* thing in the body, on its own line:

```
<!-- pfb:src=<forge|plane>,evt=<event-id> -->
```

- `src` is exactly `forge` or `plane`.
- `evt` is restricted to ASCII letters, digits, `-`, and `_`. Anything else
  is rejected at parse time so we never echo back attacker-controlled HTML.
- The leading `<!-- ` and trailing ` -->` (with exactly one space) are
  literal. Mid-body markers are ignored — only a trailing marker counts.
- Parsing tolerates trailing whitespace, a missing trailing newline, CRLF,
  and up to two blank lines between the body and the marker.

The format is a stable contract: changing it would silently disable
loop-break for in-flight events.

## Public API

```go
// Source identifies which side originated a change.
type Source string

const (
    SourceForge Source = "forge"
    SourcePlane Source = "plane"
)

type Marker struct {
    Source  Source
    EventID string
}

func Wrap(body string, src Source, eventID string) string
func Extract(body string) (Marker, bool)
func Strip(body string) string

type LRU struct { /* ... */ }

func NewLRU(cap int) *LRU
func (l *LRU) Seen(src Source, eventID, targetObjID string) bool
func (l *LRU) Record(src Source, eventID, targetObjID string) bool
```

- `Wrap` is idempotent: wrapping a body that already ends in the *same*
  marker returns it unchanged. A body ending in a *different* marker gets a
  second marker appended — we track by exact match, not by presence.
- `Wrap` returns the body unchanged if `src` or `eventID` is malformed,
  rather than emitting an invalid marker.
- `Strip` removes the trailing marker and any trailing whitespace it leaves
  behind. `Strip(Wrap(b, …))` round-trips for any well-formed body.
- `LRU.Seen` is read-only; it does not update recency. Use `Record` to
  insert. `Record` returns `false` if the entry was already present (and
  refreshes its recency).

## Concurrency

- `LRU` is safe for concurrent use; all operations take an internal mutex.
  Tested with `go test -race`.
- `Wrap`, `Extract`, and `Strip` are pure functions on their inputs and have
  no shared state.

## Dependencies

Standard library only.
