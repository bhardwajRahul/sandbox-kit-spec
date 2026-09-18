package spec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every case here is a real record from dhi.io/sbx-templates:shell-docker,
// which is what the rule has to survive: a version is published as the
// point upstream released, never as the string a distribution built
// around it.
func TestPackageVersionTakesTheUpstreamCore(t *testing.T) {
	for raw, want := range map[string]string{
		"5.2.37-2+dhi1":         "5.2.37",
		"8.14.1-2+deb13u5+dhi0": "8.14.1",
		"3.5.7-1~deb13u2+dhi1":  "3.5.7",
		"1:2.5.2-3+dhi1":        "2.5.2",
		"4:14.2.0-1+dhi0":       "14.2.0",
		"3.0.3+dhi2":            "3.0.3",
		"11.19.1-0":             "11.19.1",
		// apk spells its release as -rN.
		"5.2.21-r0": "5.2.21",

		// Fewer than three parts is what the distribution said, so it is
		// what gets published: padding would invent precision.
		"2.44-3+dhi7":       "2.44",
		"3.152+dhi1":        "3.152",
		"0.280+dhi1":        "0.280",
		"668-1+dhi1":        "668",
		"20250419+dhi2":     "20250419",
		"3:20240905-3+dhi2": "20240905",

		// A fourth part has no place in x.y.z.
		"1.3.4.20250131-1+dhi1": "1.3.4",
		"1.8.0.3-1+dhi0":        "1.8.0",
		"21.0.12.1+1-1~deb13u1": "21.0.12",

		// A leading zero is not a semver numeric identifier, but the
		// strict reading stops mid-version and would name bc 1.07.1 as
		// 1.0 — a release nobody published. Kept whole; §5.2 compares
		// 07 and 7 as one point anyway.
		"1.07.1-4+dhi0": "1.07.1",
	} {
		require.Equal(t, want, PackageVersion(raw), raw)
	}

	// Nothing a version can be built from leaves the package out
	// entirely, rather than falling back to the kit's own version —
	// which is the whole reason this exists.
	for _, raw := range []string{"", "   ", "none", "unknown", "-1", "v1.2.3", "~"} {
		require.Empty(t, PackageVersion(raw), raw)
	}
}

// dpkg sorts a tilde before everything, end of string included, so
// 1.69~deb13u1 is OLDER than 1.69 and 2.0~rc1 is the candidate rather
// than the release. Truncating there would publish a version the image
// has not reached, and §5.2 cannot carry it either — it compares a
// non-numeric segment lexically, which would put 2.0-rc1 above 2.0. So
// the package gets no entry.
func TestPackageVersionDropsAnUpstreamPrerelease(t *testing.T) {
	for _, raw := range []string{
		// Real: init-system-helpers on a shell-docker rootfs, where the
		// whole string is the upstream version.
		"1.69~deb13u1+dhi1",
		"2.0~rc1-1",
		"3.0~beta2",
		"1:2.0~rc1-1",
	} {
		require.Empty(t, PackageVersion(raw), raw)
	}

	// A tilde in the Debian revision is the ordinary rebuild marker, and
	// these really are the upstream versions they name. 20 of the 363
	// packages on that rootfs look like this, so reading the tilde
	// anywhere would throw away a twentieth of the image for nothing.
	for raw, want := range map[string]string{
		"1:9.20.26-1~deb13u1+dhi1": "9.20.26",
		"0.12.0-1~deb13u1+dhi1":    "0.12.0",
		"3.5.7-1~deb13u2+dhi1":     "3.5.7",
		// The last hyphen is the boundary, so a tilde before an earlier
		// one is still upstream's.
		"1.2-3-4~deb1": "1.2",
	} {
		require.Equal(t, want, PackageVersion(raw), raw)
	}
	require.Empty(t, PackageVersion("1.2~a-3-4"),
		"a tilde before the last hyphen is in the upstream half")
}

// The ordering claim the drop rests on, pinned: if this model ever
// ordered a prerelease correctly, carrying it would beat dropping it.
func TestThisModelCannotOrderAPrerelease(t *testing.T) {
	require.Equal(t, 1, CompareVersions("2.0-rc1", "2.0"),
		"a non-numeric segment compares lexically, so the candidate reads as newer than the release")
}

// A dpkg status file keeps a stanza for a package whose files are gone,
// so reading every stanza would have a kit provide software it does not
// carry.
func TestReadDpkgStatusTakesOnlyInstalledPackages(t *testing.T) {
	const status = `Package: bash
Status: install ok installed
Priority: required
Version: 5.2.37-2+dhi1
Description: GNU Bourne Again SHell
 A long continuation line that is not a field: Version: 9.9.9

Package: removed-but-configured
Status: deinstall ok config-files
Version: 1.0.0

Package: never-unpacked
Status: install ok not-installed

Package: jq
Status: install ok installed
Version: 1.8.2-1+dhi1`

	got, err := ReadDpkgStatus(strings.NewReader(status))
	require.NoError(t, err)
	require.Equal(t, []Package{
		{Name: "bash", Version: "5.2.37-2+dhi1"},
		{Name: "jq", Version: "1.8.2-1+dhi1"},
	}, got, "a config-files stanza is not an installed package, and the last stanza ends without a blank line")
}

func TestReadApkInstalled(t *testing.T) {
	const installed = `C:Q1eVpkasflPRfxG7bFmzR5Vn9AjuI=
P:musl
V:1.2.5-r9
A:x86_64

P:busybox
V:1.36.1-r29
A:x86_64
`

	got, err := ReadApkInstalled(strings.NewReader(installed))
	require.NoError(t, err)
	require.Equal(t, []Package{
		{Name: "musl", Version: "1.2.5-r9"},
		{Name: "busybox", Version: "1.36.1-r29"},
	}, got)
}

// One descriptor serves every platform a kit publishes, so it may only
// state what holds on all of them.
func TestDerivedProvidesStatesOnlyWhatEveryPlatformAgreesOn(t *testing.T) {
	amd64 := []Package{
		{Name: "bash", Version: "5.2.37-2+dhi1"},
		{Name: "openssl", Version: "3.5.7-1~deb13u2+dhi1"},
		{Name: "moved", Version: "1.2.3-1"},
		{Name: "amd64-only", Version: "1.0.0-1"},
	}
	arm64 := []Package{
		// The binNMU differs per architecture and normalization removes
		// it, which is what makes agreement the common case.
		{Name: "bash", Version: "5.2.37-2+dhi1+b1"},
		{Name: "openssl", Version: "3.5.7-1~deb13u2+dhi1"},
		{Name: "moved", Version: "1.2.4-1"},
	}

	require.Equal(t, []string{"deb/bash@5.2.37", "deb/openssl@3.5.7"},
		DerivedProvides(DebNamespace, [][]Package{amd64, arm64}),
		"a package only one platform carries, or one whose version moves, cannot be stated once")
}

func TestDerivedProvidesSkipsWhatItCannotName(t *testing.T) {
	got := DerivedProvides(DebNamespace, [][]Package{{
		// Dots and pluses are why the charset was widened.
		{Name: "libstdc++6", Version: "14.2.0-19+dhi0"},
		{Name: "containerd.io", Version: "1.7.28-1"},
		{Name: "python-3.14", Version: "3.14.0-1"},
		// No version this model can hold, so no entry at all.
		{Name: "mystery", Version: "not-a-version"},
		// Not a capability name however the charset is read.
		{Name: "UPPER", Version: "1.0.0"},
		{Name: "has space", Version: "1.0.0"},
	}})
	require.Equal(t, []string{
		"deb/containerd.io@1.7.28",
		"deb/libstdc++6@14.2.0",
		"deb/python-3.14@3.14.0",
	}, got)
}

// A multiarch database names one package once per architecture. That is
// one package, not agreement between platforms.
func TestDerivedProvidesFoldsAMultiarchDatabase(t *testing.T) {
	listing := []Package{
		{Name: "libc6", Version: "2.41-12+dhi1"},
		{Name: "libc6", Version: "2.41-12+dhi1"},
		{Name: "zlib1g", Version: "1:1.3.dfsg+really1.3.1-1"},
	}
	require.Equal(t, []string{"deb/libc6@2.41", "deb/zlib1g@1.3"},
		DerivedProvides(DebNamespace, [][]Package{listing, listing}))

	// Disagreeing with itself is no better than disagreeing across
	// platforms: there is still no one version to state.
	conflicting := []Package{
		{Name: "libc6", Version: "2.41-12+dhi1"},
		{Name: "libc6", Version: "2.42-1"},
	}
	require.Empty(t, DerivedProvides(DebNamespace, [][]Package{conflicting}))
}

// A record whose version this model cannot name disagrees with one it
// can, so the name is conflicted rather than answered by the other
// record. Passing over it would state a version the content does not
// carry throughout — and worse, would emit an entry the artifact's own
// check refuses, failing the build that produced it.
func TestAnUnnameableVersionConflictsTheName(t *testing.T) {
	require.Empty(t, DerivedProvides(DebNamespace, [][]Package{{
		{Name: "pkg", Version: "2.0~rc1-1"},
		{Name: "pkg", Version: "2.0-1"},
	}}), "one architecture carrying the candidate is not agreement on the release")

	// Order must not decide it either.
	require.Empty(t, DerivedProvides(DebNamespace, [][]Package{{
		{Name: "pkg", Version: "2.0-1"},
		{Name: "pkg", Version: "2.0~rc1-1"},
	}}))

	// Across listings, the same way.
	require.Empty(t, DerivedProvides(DebNamespace, [][]Package{
		{{Name: "pkg", Version: "2.0-1"}},
		{{Name: "pkg", Version: "2.0~rc1-1"}},
	}))

	// A name that is not a capability name at all is different: there is
	// nothing to state and nothing for it to disagree with, so a package
	// beside it is unaffected.
	require.Equal(t, []string{"deb/bash@5.2.37"},
		DerivedProvides(DebNamespace, [][]Package{{
			{Name: "UPPER", Version: "1.0.0-1"},
			{Name: "bash", Version: "5.2.37-2+dhi1"},
		}}))
}

// Every derived entry has to survive the grammar it is published under,
// or the frontend would assemble a descriptor its own validator refuses.
func TestDerivedProvidesParseAsProvides(t *testing.T) {
	entries := DerivedProvides(ApkNamespace, [][]Package{{
		{Name: "musl", Version: "1.2.5-r9"},
		{Name: "busybox", Version: "1.36.1-r29"},
	}})
	require.Len(t, entries, 2)
	for _, s := range entries {
		p, err := ParseProvide(s)
		require.NoError(t, err, s)
		require.True(t, IsDerivedProvide(p.Name), s)
		require.NotEmpty(t, p.Version, s)
	}
}

// §5.1 reserves the two namespaces for publishing. An author writing one
// by hand would be asserting a fact about content with nothing left to
// check it against, since the published form cannot tell the two apart.
func TestAuthoredProvidesRefuseTheReservedNamespaces(t *testing.T) {
	for _, entry := range []string{"deb/bash@5.2.37", "apk/musl@1.2.5", "deb/bash"} {
		err := RequireAuthoredProvides(&Descriptor{Provides: []string{"shell", entry}})
		require.ErrorContains(t, err, "publishing fills", entry)
	}

	// An arg reference holds no namespace to refuse until a build-phase
	// value is in it, which is why the frontend runs this on the
	// EXPANDED form: judged before expansion, the entry below is
	// unparseable and passes, and after it is a reserved namespace.
	authored := &Descriptor{Provides: []string{"${{ kit.args.cap }}"}}
	require.NoError(t, RequireAuthoredProvides(authored),
		"an unexpanded reference names no namespace yet")
	require.Error(t, RequireAuthoredProvides(&Descriptor{Provides: []string{"deb/bash@5.2.37"}}),
		"what it expands to is what has to be refused")

	// What publishing itself produces has to pass the published rules,
	// and a runtime revalidating a descriptor on load has to accept it.
	published := &Descriptor{
		SchemaVersion: SchemaVersion,
		Kind:          KindWorkload,
		DisplayName:   "Shell",
		Version:       "1.0.0",
		Provides:      []string{"shell", "deb/bash@5.2.37"},
	}
	raw, err := json.Marshal(published)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, published)
	require.NoError(t, err, "the published form legitimately carries derived entries")

	require.NoError(t, RequireAuthoredProvides(&Descriptor{Provides: []string{"shell", "com.example/deb-tools@1.0.0"}}),
		"a third party's own namespace merely containing the word is not the reserved one")
}

// A namespace is what makes com.example/gh someone's and not everyone's,
// and it does that by being a domain they own. A single label owns
// nothing, so the flat space is reserved: without that, whoever shipped
// `rpm/...` first would have taken a name this specification still needs.
func TestASingleLabelNamespaceIsReserved(t *testing.T) {
	for _, entry := range []string{"tools/gh", "rpm/bash", "mystuff/thing@1.0.0"} {
		_, err := ParseProvide(entry)
		require.ErrorContains(t, err, "single label", entry)
		require.ErrorContains(t, err, "reverse-DNS", entry)
	}

	// A dot is not enough to make a domain. These carry one and name
	// nothing, so the MUST has to read the labels rather than the
	// punctuation.
	for _, entry := range []string{"com..example/pkg", "com-.example/pkg", ".com.example/pkg", "com.example./pkg", "com.-example/pkg"} {
		_, err := ParseProvide(entry)
		require.ErrorContains(t, err, "not reverse-DNS", entry)
	}

	// Reverse-DNS is anyone's to use, and the two labels this
	// specification has defined stay usable in the published form.
	for _, entry := range []string{
		"com.example/gh", "com.docker.kit/gh", "gh", "deb/bash@5.2.37", "apk/musl@1.2.5",
		// Hyphens inside a label are what a domain may carry.
		"com.my-company/gh", "io.github.some-user/tool@1.0.0",
	} {
		_, err := ParseProvide(entry)
		require.NoError(t, err, entry)
	}

	// The rule is about the namespace, so it reaches every relation that
	// names a capability rather than provides alone.
	_, err := ParseRequire("tools/gh >= 1.0.0")
	require.ErrorContains(t, err, "single label")

	// Conflicts is the third place a capability name is judged, and it
	// goes through the descriptor validator rather than a parser.
	_, err = Validate(&Descriptor{
		SchemaVersion: SchemaVersion,
		Kind:          KindWorkload,
		DisplayName:   "Demo",
		Version:       "1.0.0",
		Conflicts:     []string{"tools/gh"},
	})
	require.ErrorContains(t, err, "single label")
}

func TestIsDerivedProvide(t *testing.T) {
	require.True(t, IsDerivedProvide("deb/bash"))
	require.True(t, IsDerivedProvide("apk/musl"))
	require.False(t, IsDerivedProvide("com.docker.kit/bash"),
		"an authored claim is not evidence read off a filesystem")
	require.False(t, IsDerivedProvide("deb"))
	require.False(t, IsDerivedProvide("com.example.deb/bash"))
}
