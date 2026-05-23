package sync

import (
	"strings"

	"github.com/hstern/plane-forge-bridge/internal/idemp"
)

// RenderDescription builds the description_html body for a Plane work item
// created or updated from a forge issue event.
//
// Format:
//
//	[optional unmapped-author preface]
//
//	<original forge body>
//
//	<!-- pfb:src=forge,evt=<delivery-id> -->
//
// Unmapped-author preface: when mapped is false (the forge sender's username
// has no entry in the users map), a one-line Markdown blockquote is
// prepended attributing the original author. When mapped is true the preface
// is omitted — Plane will attribute the work item via the assignee/created_by
// field that the engine populates separately.
//
// The loop-break marker is always present and is the last non-whitespace
// content of the body. RenderDescription delegates marker emission to
// idemp.Wrap so the canonical marker format and idempotency semantics stay in
// one place.
func RenderDescription(forgeBody, senderLogin, senderHTMLURL, repoFullName, deliveryID string, mapped bool) string {
	var b strings.Builder
	if !mapped {
		// Markdown blockquote. Shared with RenderComment via
		// unmappedAuthorPreface so the format only lives in one place.
		b.WriteString(unmappedAuthorPreface(senderLogin, senderHTMLURL, repoFullName))
	}
	b.WriteString(forgeBody)
	return idemp.Wrap(b.String(), idemp.SourceForge, deliveryID)
}
