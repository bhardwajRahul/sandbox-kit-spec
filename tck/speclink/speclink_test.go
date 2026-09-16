package speclink

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	specdocs "github.com/docker/sandbox-kit-spec/v3/docs/spec"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
	tcksandbox "github.com/docker/sandbox-kit-spec/v3/tck/sandbox"
)

// requirements is everything either suite can name in a finding.
func requirements() []string {
	return append(tckkit.Requirements(), tcksandbox.Requirements()...)
}

// TestEveryRequirementTheSuitesNameResolves is the drift catcher a link
// needs: a renamed section or a check citing an id the pages do not
// state would otherwise print an address that goes nowhere, which is
// worse than printing none.
func TestEveryRequirementTheSuitesNameResolves(t *testing.T) {
	r := New("dev")
	for _, requirement := range requirements() {
		ref, ok := r.Resolve(requirement)
		require.True(t, ok, "%q resolves to no page", requirement)
		require.NotEmpty(t, ref.URL)

		page := strings.TrimPrefix(ref.Page, "docs/spec/")
		_, err := specdocs.Pages.ReadFile(page)
		require.NoError(t, err, "%q names %s, which is not a page", requirement, ref.Page)
	}
}

// A fragment naming no heading scrolls nowhere, so the anchors are held
// to the pages they were derived from. A requirement that is a whole
// capability page has nothing finer to point at and carries none.
func TestEveryAnchorNamesAHeadingInItsPage(t *testing.T) {
	r := New("dev")
	for _, requirement := range requirements() {
		ref, ok := r.Resolve(requirement)
		require.True(t, ok)
		if ref.Anchor == "" {
			continue
		}

		page := strings.TrimPrefix(ref.Page, "docs/spec/")
		body, err := specdocs.Pages.ReadFile(page)
		require.NoError(t, err, "%q names %s", requirement, ref.Page)

		found := false
		for _, line := range strings.Split(string(body), "\n") {
			if title, isHeading := headingTitle(line); isHeading && slug(title) == ref.Anchor {
				found = true
				break
			}
		}
		require.True(t, found, "%q links to #%s, which %s has no heading for", requirement, ref.Anchor, page)
	}
}

// A statement id is finer than the section around it, and resolving it
// to the section's own heading would hand a reader the top of a page to
// search. It resolves through the anchor the page carries.
func TestAStatementResolvesThroughItsAnchor(t *testing.T) {
	ref, ok := New("dev").Resolve("lifecycle@1/install-once")
	require.True(t, ok)
	require.Equal(t, "docs/spec/capabilities/com.docker.sandbox/lifecycle@1.md", ref.Page)
	require.Equal(t, "runtime-behavior", ref.Anchor)
}

func TestASectionResolvesByNumber(t *testing.T) {
	ref, ok := New("dev").Resolve("SPEC-v3 §9.3")
	require.True(t, ok)
	require.Equal(t, "docs/spec/SPEC-v3.md", ref.Page)
	require.Equal(t, "93-annotations", ref.Anchor)
}

// A released binary quotes the text it was built from, so its links
// point at that tag rather than at whatever main has become.
func TestLinksPointAtTheVersionTheyWereBuiltFrom(t *testing.T) {
	require.Contains(t, New("dev").URL("SPEC-v3 §10"), "/blob/main/")
	require.Contains(t, New("3.1.0").URL("SPEC-v3 §10"), "/blob/v3.1.0/")
	require.Contains(t, New("v3.1.0").URL("SPEC-v3 §10"), "/blob/v3.1.0/")
}

// An id from a suite newer than the pages this binary carries has no
// home here, and inventing one would be worse than saying so.
func TestAnUnknownRequirementDoesNotResolve(t *testing.T) {
	r := New("dev")
	for _, requirement := range []string{"", "made-up", "SPEC-v4 §1", "not-a-capability/statement"} {
		_, ok := r.Resolve(requirement)
		require.False(t, ok, "%q resolved", requirement)
		require.Empty(t, r.URL(requirement))
	}
}
