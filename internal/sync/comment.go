package sync

import (
	"strings"

	"github.com/hstern/plane-forge-bridge/internal/idemp"
)

// RenderComment builds the body for a comment mirrored from one side to
// the other. Format mirrors RenderDescription:
//
//	[optional unmapped-author preface]
//
//	<original comment body>
//
//	<!-- pfb:src=<src>,evt=<delivery-id> -->
//
// The trailing loop-break marker is always present and is the last
// non-whitespace content. Emission is delegated to idemp.Wrap so the
// canonical marker format and idempotency semantics live in one place.
//
// When mapped is false the unmapped-author preface is prepended; both
// RenderDescription and RenderComment share unmappedAuthorPreface so the
// blockquote format stays in sync if it changes.
func RenderComment(originalBody, senderLogin, senderHTMLURL, repoFullName, deliveryID string, src idemp.Source, mapped bool) string {
	var b strings.Builder
	if !mapped {
		b.WriteString(unmappedAuthorPreface(senderLogin, senderHTMLURL, repoFullName))
	}
	b.WriteString(originalBody)
	return idemp.Wrap(b.String(), src, deliveryID)
}

// unmappedAuthorPreface returns the one-line Markdown blockquote prepended
// to any body whose sender has no entry in the identity map.
//
// Format is intentionally identical between RenderDescription and
// RenderComment — see TestRenderComment_PrefaceHelperSharedWithRenderDescription
// for the regression guard.
func unmappedAuthorPreface(senderLogin, senderHTMLURL, repoFullName string) string {
	var b strings.Builder
	b.WriteString("> Originally posted by `")
	b.WriteString(senderLogin)
	b.WriteString("` on [")
	b.WriteString(repoFullName)
	b.WriteString("](")
	b.WriteString(senderHTMLURL)
	b.WriteString(")\n>\n\n")
	return b.String()
}
