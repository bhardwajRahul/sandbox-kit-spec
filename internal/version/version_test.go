package version

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The stamp a release links in has to survive to both surfaces: the tag
// on its own, which is what a spec link resolves against, and the human
// form beside the abbreviated commit.
func TestStampedBuildReportsItsReleaseAndCommit(t *testing.T) {
	linked(t, "3.0.0-m.5", "2f9a1c4e8b7d6a5c4b3e2d1f0a9b8c7d6e5f4a3b")
	require.Equal(t, "3.0.0-m.5", Tag())
	require.Equal(t, "2f9a1c4e8b7d6a5c4b3e2d1f0a9b8c7d6e5f4a3b", Rev(),
		"the artifact records the whole commit; only the display is shortened")
	require.Equal(t, "3.0.0-m.5 (2f9a1c4e)", String())
}

// A build no release stamped says so rather than naming a version it is
// not. Nothing Go records for a test binary can answer here — a
// pseudo-version and "(devel)" are both refused — so the word is reached
// whatever the tree looks like.
func TestUnstampedBuildCallsItselfDev(t *testing.T) {
	linked(t, "", "b7d4f43c47d24337aa23eda6456b1b6a78d3b862")
	require.Equal(t, development, Tag())
	require.Equal(t, "dev (b7d4f43c)", String(),
		"a revision alone still makes a development build traceable")
}

// A linker stamp is the release's own claim and outranks what Go
// recorded; each field falls back on its own, since the two sources
// answer different halves of the question.
func TestFillPrefersTheLinkedStamp(t *testing.T) {
	recorded := stamp{version: "2.0.0", revision: "aaaa"}

	require.Equal(t, stamp{version: "3.0.0", revision: "bbbb"},
		fill(stamp{version: "3.0.0", revision: "bbbb"}, recorded))
	require.Equal(t, stamp{version: "2.0.0", revision: "bbbb"},
		fill(stamp{revision: "bbbb"}, recorded))
	require.Equal(t, stamp{version: "3.0.0", revision: "aaaa"},
		fill(stamp{version: "3.0.0"}, recorded))
	require.Equal(t, recorded, fill(stamp{}, recorded))
}

// Without a revision there is no parenthesis to leave empty.
func TestDisplayOmitsAnAbsentRevision(t *testing.T) {
	require.Equal(t, "3.0.0", display("3.0.0", ""))
	require.Equal(t, "3.0.0 (2f9a1c4e)", display("3.0.0", "2f9a1c4e8b7d6a5c"))
}

// An edited tree is not the commit it names, and the marker that says so
// has to survive abbreviation — truncating it away would turn the one
// honest signal into a claim of cleanliness.
func TestDirtyTreeKeepsItsMarker(t *testing.T) {
	require.Equal(t, "b7d4f43c-dirty", abbreviate("b7d4f43c47d24337aa23eda6456b1b6a78d3b862-dirty"))
	require.Equal(t, "b7d4f43c", abbreviate("b7d4f43c47d24337aa23eda6456b1b6a78d3b862"))
	require.Equal(t, "b7d4", abbreviate("b7d4"), "a revision shorter than the cut is left alone")
}

// Go derives a pseudo-version from the commit when it builds inside the
// repository. It is accurate and useless as a link target, so it must not
// reach Tag, where a caller would resolve it against a ref that does not
// exist.
func TestPseudoVersionsAreNotReleases(t *testing.T) {
	for _, version := range []string{
		// The three shapes: no base tag, a prerelease base, a release base.
		"v0.0.0-20260922090216-b7d4f43c47d2",
		"v3.0.0-m.5.0.20260922090216-b7d4f43c47d2",
		"v3.0.1-0.20260922090216-b7d4f43c47d2",
		// Build metadata marks a version Go qualified itself.
		"v3.0.0-m.5.0.20260922090216-b7d4f43c47d2+dirty",
		"v2.0.0+incompatible",
		// Go's word for an untagged main module.
		"(devel)",
		"",
	} {
		require.Empty(t, releaseTag(version), "%q is not a release tag", version)
	}
}

// The versions a real install carries have to survive the same filter,
// including prereleases, which the repository tags routinely.
func TestReleaseTagsSurvive(t *testing.T) {
	for version, want := range map[string]string{
		"v3.0.0":        "3.0.0",
		"v3.0.0-m.5":    "3.0.0-m.5",
		"v3.0.0-rc.1":   "3.0.0-rc.1",
		"3.0.0":         "3.0.0",
		"v10.20.30-m.1": "10.20.30-m.1",
	} {
		require.Equal(t, want, releaseTag(version), "for %q", version)
	}
}

// A near miss must not be read as a pseudo-version: the timestamp is 14
// digits and the revision 12 hex characters, and a prerelease that merely
// has three dash-separated fields is an ordinary tag.
func TestPseudoVersionShapeIsExact(t *testing.T) {
	for _, version := range []string{
		"3.0.0-alpha-beta",
		"3.0.0-2026092209021-b7d4f43c47d2",  // 13-digit timestamp
		"3.0.0-20260922090216-b7d4f43c47d",  // 11-character revision
		"3.0.0-20260922090216-b7d4f43c47dz", // not hex
	} {
		require.False(t, isPseudoVersion(version), "%q is not a pseudo-version", version)
	}
	require.True(t, isPseudoVersion("3.0.0-20260922090216-b7d4f43c47d2"))
}

// linked installs a link-time stamp for one test, clearing the memoized
// answer so the values under test are the ones that get read.
func linked(t *testing.T, version, revision string) {
	t.Helper()
	previousVersion, previousRevision := Version, Revision
	Version, Revision = version, revision
	reset()
	t.Cleanup(func() {
		Version, Revision = previousVersion, previousRevision
		reset()
	})
}
