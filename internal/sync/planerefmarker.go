package sync

import "regexp"

// PlaneRefMarker is the durable reverse-lookup marker the bridge appends
// to forge issues mirrored from plane. It is distinct from the loop-break
// marker emitted by idemp.Wrap, which carries the originating EVENT id
// and is consumed by idemp.Extract on the inbound path. This marker
// names the source plane WORK ITEM and is stable across re-deliveries,
// so future plane → forge updates can find the mirrored forge issue by
// scanning forge issue bodies for the marker.
//
// Format:
//
//	<!-- pfb:plane-workitem=<workspace_id>/<project_id>/<work_item_id> -->
//
// Whitespace inside the marker matches the idemp loop-break marker
// (single space after the opening "<!--" and before "-->") so an
// operator reading the body sees a consistent shape.
//
// Step 10 limitation: forge.Client does not yet expose a SearchIssues
// endpoint, so this marker is currently write-only — emitted on every
// create from plane, but not yet read back to short-circuit the create
// path on a plane work_item.updated redelivery. See README's "Open
// questions" for the deferral.
type PlaneRefMarker struct {
	WorkspaceID string
	ProjectID   string
	WorkItemID  string
}

// planeRefMarkerRe matches a PlaneRefMarker anywhere in a body. The IDs
// are restricted to characters that appear in UUIDs (the Plane shape
// for all three) plus the few non-UUID characters Plane has been seen
// to use in workspace identifiers. The opening "<!-- " and trailing
// " -->" exactly mirror the idemp marker grammar so a future operator
// reading body source can pattern-match either with the same regex
// fragment.
var planeRefMarkerRe = regexp.MustCompile(
	`<!--[ \t]+pfb:plane-workitem=([A-Za-z0-9_-]+)/([A-Za-z0-9_-]+)/([A-Za-z0-9_-]+)[ \t]+-->`,
)

// RenderPlaneRefMarker formats a marker for embedding in a forge issue
// body. The fields are written verbatim; the caller must have validated
// them upstream. The format is the inverse of ExtractPlaneRefMarker, and
// the two are round-trip tested.
func RenderPlaneRefMarker(m PlaneRefMarker) string {
	return "<!-- pfb:plane-workitem=" + m.WorkspaceID + "/" + m.ProjectID + "/" + m.WorkItemID + " -->"
}

// ExtractPlaneRefMarker pulls the first PlaneRefMarker from body, if
// present. Returns ok=false on a body with no marker or on a marker
// whose ID fields contain characters outside the [A-Za-z0-9_-] grammar
// (which is what the renderer emits). A malformed marker is treated as
// "no marker" so callers can fall through to the create path rather
// than crashing on operator-edited bodies.
func ExtractPlaneRefMarker(body string) (PlaneRefMarker, bool) {
	m := planeRefMarkerRe.FindStringSubmatch(body)
	if m == nil {
		return PlaneRefMarker{}, false
	}
	return PlaneRefMarker{
		WorkspaceID: m[1],
		ProjectID:   m[2],
		WorkItemID:  m[3],
	}, true
}
