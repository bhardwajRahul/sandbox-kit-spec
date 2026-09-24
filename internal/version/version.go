package version

import (
	"runtime/debug"
	"strings"
	"sync"
)

// Name is the frontend's published image, which is also how a kit names
// what built it. Stated here rather than at the annotation's call site so
// the binary and the artifact cannot disagree about it.
const Name = "docker/sandbox-kit"

// Set at link time:
//
//	-ldflags "-X github.com/docker/sandbox-kit-spec/v3/internal/version.Version=3.0.0
//	          -X github.com/docker/sandbox-kit-spec/v3/internal/version.Revision=<sha>"
//
// Empty is the honest default: a build nobody stamped should say so
// rather than name a version it is not.
var (
	Version  = ""
	Revision = ""
)

// development is what an unstamped build calls itself. speclink reads the
// same word to mean "link at main", so a development binary cites the
// text it was built from rather than a tag it predates.
const development = "dev"

// short is the revision length a human reads. The full sha is what the
// artifact records; on screen it is noise past the first few characters.
const short = 8

// stamp is the answer: a release, and the commit it was built from.
// Either may be empty, and each is filled from its own source.
type stamp struct{ version, revision string }

var resolve = sync.OnceValue(compute)

func compute() stamp {
	return fill(stamp{version: Version, revision: Revision}, recorded())
}

// fill completes a linker stamp from what Go recorded, field by field. A
// linker stamp wins wherever it exists: it is the release's own claim,
// and the only one a build that cannot see .git is able to make.
func fill(linked, recorded stamp) stamp {
	if linked.version == "" {
		linked.version = recorded.version
	}
	if linked.revision == "" {
		linked.revision = recorded.revision
	}
	return linked
}

// recorded is what Go wrote into the binary by itself, which covers the
// two cases no builder stamps: `go install …@v3.0.0`, where the module
// version is the release, and a local `go build` inside the repository,
// where the VCS settings name the commit.
func recorded() stamp {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return stamp{}
	}
	var s stamp
	s.version = releaseTag(info.Main.Version)
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			s.revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	// A build from an edited tree is not the commit it names, and a
	// stamp that hides that is worse than none.
	if modified && s.revision != "" {
		s.revision += "-dirty"
	}
	return s
}

// releaseTag keeps a module version only when it names a release someone
// can look up, and drops anything Go synthesized. Building inside the
// repository yields a pseudo-version derived from the commit — accurate,
// but not a tag: it resolves to nothing, spec links built from it 404,
// and the revision recorded beside it already says more. Such a build is
// a development build, which is what it should call itself.
func releaseTag(version string) string {
	version = strings.TrimPrefix(version, "v")
	// "(devel)" is Go's word for an untagged main module; build metadata
	// marks a version Go qualified itself, "+dirty" and "+incompatible"
	// alike.
	if version == "" || version == "(devel)" || strings.Contains(version, "+") {
		return ""
	}
	if isPseudoVersion(version) {
		return ""
	}
	return version
}

// isPseudoVersion matches the shape Go derives from a commit: whatever
// base it starts from, it ends with a 14-digit UTC timestamp and a
// 12-character revision prefix.
func isPseudoVersion(version string) bool {
	dash := strings.LastIndex(version, "-")
	if dash < 0 {
		return false
	}
	revision := version[dash+1:]
	if len(revision) != 12 || !isAll(revision, isHex) {
		return false
	}
	// The timestamp is the field before the revision, but which
	// separator ends it depends on the base: Go joins it with a dash to
	// a plain release and with a dot to one that already has a
	// prerelease, so either closes the field.
	base := version[:dash]
	timestamp := base[strings.LastIndexAny(base, ".-")+1:]
	return len(timestamp) == 14 && isAll(timestamp, isDigit)
}

func isAll(s string, class func(byte) bool) bool {
	for i := range len(s) {
		if !class(s[i]) {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool { return isDigit(c) || (c >= 'a' && c <= 'f') }

// Tag is the released version alone — "3.0.0-m.5", or "dev" for a build
// no release stamped. Callers resolving it against a ref want this and
// not String: a revision is not a tag, and a URL built from one 404s.
func Tag() string {
	if v := resolve().version; v != "" {
		return v
	}
	return development
}

// Rev is the full commit the binary was built from, empty when the build
// recorded none. The whole sha, not the abbreviation String shows: an
// artifact records what a reader can look up.
func Rev() string { return resolve().revision }

// String is the stamp a human reads: "3.0.0-m.5 (2f9a1c4e)".
func String() string { return display(Tag(), Rev()) }

// display writes a version and its revision together, leaving out the
// parenthesis where there is no revision to put in it.
func display(version, revision string) string {
	if revision == "" {
		return version
	}
	return version + " (" + abbreviate(revision) + ")"
}

// abbreviate shortens a sha for display while leaving anything that is
// not one — a "-dirty" suffix — whole. Truncating that marker away would
// turn the one honest signal a dirty build carries into a claim of
// cleanliness.
func abbreviate(revision string) string {
	sha, suffix, found := strings.Cut(revision, "-")
	if len(sha) > short {
		sha = sha[:short]
	}
	if found {
		return sha + "-" + suffix
	}
	return sha
}
