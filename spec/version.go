package spec

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Names are constrained like kit handles: lowercase alphanumeric plus
// hyphens, no leading or trailing hyphen. An optional dotted namespace
// prefix qualifies a capability name the way registries qualify image
// names.
//
// A capability name admits dots and pluses on top of that, because a
// distribution package name is a capability name here (§9.6): Debian
// ships libstdc++6, python-3.14 and containerd.io, which is one package
// in twenty on a shell-docker rootfs and none of them nameable without.
// A handle stays narrow — it is an identifier someone types and a store
// keys on, with no filesystem to answer to.
var (
	handleName     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)
	capabilityName = regexp.MustCompile(`^[a-z0-9]([a-z0-9+.-]{0,62}[a-z0-9+])?$`)
	// capabilityNamespace is the charset a namespace draws on, and only
	// that. Its structure is judged separately, label by label, because
	// no single pattern over the whole namespace can tell com.example
	// from com..example — both are dots and letters in some order.
	capabilityNamespace = regexp.MustCompile(`^[a-z0-9.-]+$`)
	// namespaceLabel is one dot-separated piece: the shape a DNS label
	// has, alphanumeric at both ends with hyphens allowed inside, and
	// within the 63 characters a label may occupy.
	namespaceLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// maxNamespaceLength is what a domain name occupies at most, which a
// reversed one occupies too. Bounded for the same reason the labels are:
// a namespace is someone's because the domain is theirs, and a string no
// domain could be is nobody's to own.
const maxNamespaceLength = 253

// DefaultCapabilityNamespace qualifies bare capability names, the way
// docker.io/library qualifies bare image names: `gh` and
// `com.docker.kit/gh` are the same capability, while a third party's
// `com.example/gh` never matches either. Deliberately a sibling of
// com.docker.sandbox, not the same namespace — com.docker.sandbox names
// contracts the RUNTIME answers (capability types), com.docker.kit names
// vocabulary KITS provide.
const DefaultCapabilityNamespace = "com.docker.kit"

// DebNamespace and ApkNamespace hold what a kit's own filesystem says it
// carries, rather than what its author wrote down: publishing reads the
// package databases and states one entry per installed package (§9.6).
//
// Their own namespaces, and not com.docker.kit, because they are facts of
// a different kind and a different authority. A bare `node` is a claim an
// author makes about what the kit offers; `deb/nodejs-24` is what dpkg
// records, and the two must not collide in one namespace where a
// requirement could match either.
const (
	DebNamespace = "deb"
	ApkNamespace = "apk"
)

// IsDerivedProvide reports whether a normalized capability name is one
// publishing derived from image content rather than one an author wrote.
//
// Three callers need the distinction, all of them because a derived entry
// is evidence and an authored one is a claim: the version annotation must
// not be drowned out by hundreds of package versions, the derivation must
// replace its own previous output rather than accumulate it, and a report
// reads better when it can say which is which.
func IsDerivedProvide(name string) bool {
	ns, _, qualified := strings.Cut(name, "/")
	return qualified && (ns == DebNamespace || ns == ApkNamespace)
}

// NormalizeCapabilityName qualifies a bare capability name with the
// default namespace; already-qualified names pass through. Matching,
// locks, and surfaces speak normalized names; authored short forms are
// display sugar.
func NormalizeCapabilityName(name string) string {
	if strings.Contains(name, "/") {
		return name
	}
	return DefaultCapabilityNamespace + "/" + name
}

// DisplayCapabilityName renders a normalized name for humans: the
// default namespace is implied and stripped, anything else stays
// qualified — mirroring how image names display.
func DisplayCapabilityName(name string) string {
	return strings.TrimPrefix(name, DefaultCapabilityNamespace+"/")
}

// reservedNamespaces are the single-label namespaces this specification
// defines (§5.1). Everything else a namespace can be is reverse-DNS, so
// these are the only ones no domain stands behind — which is why the
// space is reserved rather than first-come: it is what keeps a label
// this specification has not defined yet available to define.
var reservedNamespaces = map[string]bool{
	DebNamespace: true,
	ApkNamespace: true,
}

// validCapabilityName accepts a bare name or a namespace-qualified one.
func validCapabilityName(name string) bool {
	return capabilityNameError(name) == nil
}

// capabilityNameError says why a name is not a capability name, or nil
// when it is one. Separate from the predicate because a namespace this
// specification reserved is refused for a reason an author can act on,
// and "invalid capability name" would not say what to do about it.
func capabilityNameError(name string) error {
	ns, base, qualified := strings.Cut(name, "/")
	if !qualified {
		if !capabilityName.MatchString(name) {
			return fmt.Errorf("invalid capability name %q", name)
		}
		return nil
	}
	if ns == "" {
		return fmt.Errorf("invalid capability name %q", name)
	}
	// Charset first, so a namespace that is not even lowercase reads as
	// the malformed name it is rather than as a domain-shape complaint.
	if !capabilityNamespace.MatchString(ns) || !capabilityName.MatchString(base) {
		return fmt.Errorf("invalid capability name %q", name)
	}
	// Then the structure. A namespace someone defines is reverse-DNS
	// (§5.1), which is what makes com.example/gh theirs and nobody
	// else's — so every label has to be one a domain could carry. A dot
	// with nothing either side of it does not make com..example a
	// domain for having a dot in it.
	if len(ns) > maxNamespaceLength {
		// Not echoed: the offending value is the length.
		return fmt.Errorf("namespace in %q is %d characters, and no domain is longer than %d",
			base, len(ns), maxNamespaceLength)
	}
	labels := strings.Split(ns, ".")
	for _, label := range labels {
		if !namespaceLabel.MatchString(label) {
			return fmt.Errorf("namespace %q in %q is not reverse-DNS: %q is not a label a domain can carry", ns, name, label)
		}
	}
	// The labels this specification defined itself are single, and stand
	// outside the two-label rule rather than failing it.
	if reservedNamespaces[ns] {
		return nil
	}
	if len(labels) < 2 {
		return fmt.Errorf("namespace %q in %q is a single label, which this specification reserves; a namespace of your own is reverse-DNS, as in com.example/%s", ns, name, base)
	}
	return nil
}

// Provide is a parsed provides entry: a capability name with an optional
// version.
type Provide struct {
	Name    string
	Version string // empty when unversioned
}

// ConstraintOp is a comparison against a version point.
type ConstraintOp string

const (
	OpGTE ConstraintOp = ">="
	OpGT  ConstraintOp = ">"
	OpLTE ConstraintOp = "<="
	OpLT  ConstraintOp = "<"
	OpEQ  ConstraintOp = "="
)

// Constraint is one comparison in a require: Op Version.
type Constraint struct {
	Op      ConstraintOp
	Version string
}

// Require is a parsed requires/integrates entry: a capability name with
// zero or more version constraints. Empty Constraints means any provider
// of Name satisfies. Multiple constraints are ANDed (comma-separated in
// the authored form). Ranges and pins change only the Satisfies
// predicate: resolution stays a closed-set check, never a backtracking
// solver — the set already names its providers, and §5.3 admits at most
// one provider per capability name.
type Require struct {
	Name        string
	Constraints []Constraint // empty when unconstrained
}

// ParseProvide parses "name" or "name@version". The parsed Name is
// normalized: bare names gain the default namespace.
func ParseProvide(s string) (Provide, error) {
	name, version, found := strings.Cut(strings.TrimSpace(s), "@")
	if err := capabilityNameError(name); err != nil {
		return Provide{}, fmt.Errorf("provides entry %q: %w", s, err)
	}
	if found {
		if _, err := parseVersion(version); err != nil {
			return Provide{}, fmt.Errorf("provides entry %q: %w", s, err)
		}
	}
	return Provide{Name: NormalizeCapabilityName(name), Version: version}, nil
}

// CanonicalRequire renders a parsed require in one spelling, so two
// entries meaning the same thing compare equal however they were
// written: "shell >= 1.0.0" and "shell>=1.0.0" and a bare name beside
// its namespace-qualified form are one relation, and the order two
// constraints were listed in is not part of what they say.
func CanonicalRequire(r Require) string {
	rendered := make([]string, 0, 2)
	for _, c := range simplifyConstraints(r.Constraints) {
		rendered = append(rendered, string(c.Op)+canonicalVersion(c.Version))
	}
	sort.Strings(rendered)
	return r.Name + " " + strings.Join(rendered, ",")
}

// simplifyConstraints reduces a set to what it actually says: a pin
// answers everything, and otherwise only the tightest bound in each
// direction constrains anything. ">= 1.0.0, >= 2.0.0" is ">= 2.0.0",
// and rendering the two differently would let one spelling of a
// relation pass a check the other fails.
func simplifyConstraints(cs []Constraint) []Constraint {
	for _, c := range cs {
		if c.Op == OpEQ {
			return []Constraint{c}
		}
	}
	// Exclusive bounds are rewritten as the inclusive bounds they
	// equal, so one range has one rendering. Below, "> x" admits from
	// next(x) up. Above, an exclusive bound only converts when it
	// names a successor — "< next(x)" admits up to x — because the
	// largest version below an arbitrary one has no finite spelling,
	// which is why "< 1.0.1" stays as written.
	normalized := make([]Constraint, 0, len(cs))
	for _, c := range cs {
		switch {
		case c.Op == OpGT:
			c = Constraint{Op: OpGTE, Version: nextVersion(c.Version)}
		case c.Op == OpLT && strings.HasSuffix(c.Version, versionSuccessorSuffix):
			c = Constraint{Op: OpLTE, Version: strings.TrimSuffix(c.Version, versionSuccessorSuffix)}
		}
		normalized = append(normalized, c)
	}
	cs = normalized
	var lo, hi *Constraint
	for i := range cs {
		c := cs[i]
		switch c.Op {
		case OpGTE, OpGT:
			// The tightest lower bound: a higher version, or the same
			// version excluded where the other included it.
			if lo == nil || CompareVersions(c.Version, lo.Version) > 0 ||
				(CompareVersions(c.Version, lo.Version) == 0 && c.Op == OpGT) {
				lo = &cs[i]
			}
		case OpLTE, OpLT:
			if hi == nil || CompareVersions(c.Version, hi.Version) < 0 ||
				(CompareVersions(c.Version, hi.Version) == 0 && c.Op == OpLT) {
				hi = &cs[i]
			}
		}
	}
	// Bounds that meet inclusively name one version, which is what a
	// pin says: rendering them as two would make "= 1.0.0" and
	// ">= 1.0.0, <= 1.0.0" different spellings of one relation.
	if lo != nil && hi != nil && lo.Op == OpGTE && hi.Op == OpLTE &&
		CompareVersions(lo.Version, hi.Version) == 0 {
		return []Constraint{{Op: OpEQ, Version: lo.Version}}
	}
	out := make([]Constraint, 0, 2)
	if lo != nil {
		out = append(out, *lo)
	}
	if hi != nil {
		out = append(out, *hi)
	}
	return out
}

// canonicalVersion strips the leading zeros that CompareVersions
// looks past, so two spellings of one point render alike.
func canonicalVersion(v string) string {
	segments := splitVersion(v)
	for i, seg := range segments {
		if !allDigits(seg) {
			continue
		}
		if trimmed := strings.TrimLeft(seg, "0"); trimmed != "" {
			segments[i] = trimmed
		} else {
			segments[i] = "0"
		}
	}
	return strings.Join(segments, ".")
}

// ParseRequire parses a requires/integrates entry:
//
//	name
//	name <op> version
//	name <op> version, <op> version, ...
//
// where <op> is one of >=, >, <=, <, =. Whitespace around operators and
// commas is optional. The legacy form "name >= version" is unchanged.
// Unsatisfiable constraint sets (conflicting pins, inverted bounds) are
// rejected at parse. The parsed Name is normalized: bare names gain the
// default namespace.
func ParseRequire(s string) (Require, error) {
	s = strings.TrimSpace(s)
	name, rest, err := splitRequireName(s)
	if err != nil {
		return Require{}, fmt.Errorf("requires entry %q: %w", s, err)
	}
	if err := capabilityNameError(name); err != nil {
		return Require{}, fmt.Errorf("requires entry %q: %w", s, err)
	}
	req := Require{Name: NormalizeCapabilityName(name)}
	if rest == "" {
		return req, nil
	}
	constraints, err := parseConstraints(rest)
	if err != nil {
		return Require{}, fmt.Errorf("requires entry %q: %w", s, err)
	}
	req.Constraints = constraints
	return req, nil
}

// splitRequireName cuts a require string into the capability name and the
// optional constraint tail. Capability names never contain comparison
// operators, so the first operator marks the boundary.
func splitRequireName(s string) (name, rest string, err error) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '>', '<', '=':
			name = strings.TrimSpace(s[:i])
			rest = strings.TrimSpace(s[i:])
			if name == "" {
				return "", "", fmt.Errorf("missing capability name")
			}
			return name, rest, nil
		}
	}
	return strings.TrimSpace(s), "", nil
}

func parseConstraints(s string) ([]Constraint, error) {
	var out []Constraint
	for s != "" {
		op, afterOp, err := cutConstraintOp(s)
		if err != nil {
			return nil, err
		}
		versionPart, next, more := strings.Cut(afterOp, ",")
		versionPart = strings.TrimSpace(versionPart)
		if versionPart == "" {
			return nil, fmt.Errorf("constraint %q: missing version", op)
		}
		version, err := parseVersion(versionPart)
		if err != nil {
			return nil, fmt.Errorf("constraint %q: %w", op, err)
		}
		out = append(out, Constraint{Op: op, Version: version})
		if !more {
			break
		}
		s = strings.TrimSpace(next)
		if s == "" {
			return nil, fmt.Errorf("trailing comma in constraints")
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty constraints")
	}
	if err := checkConstraintsSatisfiable(out); err != nil {
		return nil, err
	}
	return out, nil
}

// checkConstraintsSatisfiable rejects constraint sets no version can meet:
// conflicting pins, a pin outside other bounds, or a lower bound above
// (or exclusively equal to) an upper bound. Detected at parse so a kit
// cannot publish a require that can never resolve.
func checkConstraintsSatisfiable(cs []Constraint) error {
	var pin string
	pinned := false
	for _, c := range cs {
		if c.Op != OpEQ {
			continue
		}
		if pinned && CompareVersions(pin, c.Version) != 0 {
			return fmt.Errorf("unsatisfiable constraints: = %s and = %s", pin, c.Version)
		}
		pin = c.Version
		pinned = true
	}
	if pinned {
		for _, c := range cs {
			if !constraintHolds(pin, c) {
				return fmt.Errorf("unsatisfiable constraints: = %s conflicts with %s %s", pin, c.Op, c.Version)
			}
		}
		return nil
	}

	// The grammar's own floor: a version begins with a nonnegative
	// numeric segment, so 0 is an inclusive lower bound every
	// constraint set carries whether or not one was written. Without
	// it an upper bound like "< 0" reads as unconstrained below and
	// passes, though nothing can satisfy it.
	var (
		loVer = "0"
		loExc = false
		loSet = true
		hiVer string
		hiExc bool
		hiSet bool
	)
	for _, c := range cs {
		switch c.Op {
		case OpGTE, OpGT:
			exc := c.Op == OpGT
			if !loSet || CompareVersions(c.Version, loVer) > 0 ||
				(CompareVersions(c.Version, loVer) == 0 && exc && !loExc) {
				loVer, loExc, loSet = c.Version, exc, true
			}
		case OpLTE, OpLT:
			exc := c.Op == OpLT
			if !hiSet || CompareVersions(c.Version, hiVer) < 0 ||
				(CompareVersions(c.Version, hiVer) == 0 && exc && !hiExc) {
				hiVer, hiExc, hiSet = c.Version, exc, true
			}
		}
	}
	if !loSet || !hiSet {
		return nil
	}
	// An exclusive lower bound admits the next version, not the one
	// named — and this order is not dense, so "next" is a version that
	// can be written down. Comparing endpoints alone called
	// "> 0, < 0.-" satisfiable, though nothing lies between them.
	effectiveLo, effectiveExc := loVer, loExc
	if loExc {
		effectiveLo, effectiveExc = nextVersion(loVer), false
	}
	cmp := CompareVersions(effectiveLo, hiVer)
	if cmp > 0 || (cmp == 0 && (effectiveExc || hiExc)) {
		loOp, hiOp := OpGTE, OpLTE
		if loExc {
			loOp = OpGT
		}
		if hiExc {
			hiOp = OpLT
		}
		return fmt.Errorf("unsatisfiable constraints: %s %s and %s %s", loOp, loVer, hiOp, hiVer)
	}
	return nil
}

// nextVersion is the smallest version greater than v.
//
// Appending a segment is what makes one: a further segment always
// sorts above the version without it, and "-" is the smallest segment
// that can be written, being the one character below every digit and
// letter the grammar admits and shorter than anything else starting
// with it. So v + ".-" has nothing between it and v, which is what
// lets an exclusive bound be reasoned about as an inclusive one.
func nextVersion(v string) string {
	return v + versionSuccessorSuffix
}

// versionSuccessorSuffix is what makes one version the next: a further
// segment sorts above the version without it, and "-" is the smallest
// segment the grammar can write.
const versionSuccessorSuffix = ".-"

func cutConstraintOp(s string) (ConstraintOp, string, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, ">="):
		return OpGTE, strings.TrimSpace(s[2:]), nil
	case strings.HasPrefix(s, "<="):
		return OpLTE, strings.TrimSpace(s[2:]), nil
	case strings.HasPrefix(s, ">"):
		return OpGT, strings.TrimSpace(s[1:]), nil
	case strings.HasPrefix(s, "<"):
		return OpLT, strings.TrimSpace(s[1:]), nil
	case strings.HasPrefix(s, "="):
		return OpEQ, strings.TrimSpace(s[1:]), nil
	default:
		return "", "", fmt.Errorf("expected version operator, got %q", s)
	}
}

// Satisfies reports whether the provide meets the require: same capability
// name, and a provided version that holds every stated constraint. An
// unversioned provide satisfies only an unconstrained require, so a
// constraint never silently matches a capability that declared no version.
func Satisfies(p Provide, r Require) bool {
	if p.Name != r.Name {
		return false
	}
	if len(r.Constraints) == 0 {
		return true
	}
	if p.Version == "" {
		return false
	}
	for _, c := range r.Constraints {
		if !constraintHolds(p.Version, c) {
			return false
		}
	}
	return true
}

func constraintHolds(version string, c Constraint) bool {
	cmp := CompareVersions(version, c.Version)
	switch c.Op {
	case OpGTE:
		return cmp >= 0
	case OpGT:
		return cmp > 0
	case OpLTE:
		return cmp <= 0
	case OpLT:
		return cmp < 0
	case OpEQ:
		return cmp == 0
	default:
		return false
	}
}

// CompareVersions compares dotted numeric versions, returning -1, 0, or 1.
// Non-numeric segments compare lexically, after numeric segments of equal
// position compare numerically — enough for release tags without importing
// a semver dependency.
func CompareVersions(a, b string) int {
	as := splitVersion(a)
	bs := splitVersion(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		switch {
		case allDigits(av) && allDigits(bv):
			// Compared as digit strings rather than through a
			// machine-sized integer: a version segment has no length
			// bound, and a conversion that overflowed would fall back
			// to a lexical order in which "99999999999999999999" is
			// greater than "100000000000000000000".
			if c := compareDigits(av, bv); c != 0 {
				return c
			}
		default:
			if av != bv {
				if av < bv {
					return -1
				}
				return 1
			}
		}
	}
	return 0
}

// allDigits reports whether a segment is a number, which an empty
// segment (a shorter version padded against a longer one) is not.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// compareDigits orders two digit strings by value: more digits is
// larger once leading zeros are gone, and equal lengths compare
// lexically, which for digits is the same as numerically.
func compareDigits(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func splitVersion(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return strings.Split(v, ".")
}

// versionPattern accepts dotted numeric versions with no "v" prefix: a
// version names a point in an order, and admitting two spellings of the
// same point ("1.2.3" and "v1.2.3") would make equality depend on
// normalization every consumer has to remember.
var versionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9A-Za-z-]+)*$`)

func parseVersion(v string) (string, error) {
	v = strings.TrimSpace(v)
	if !versionPattern.MatchString(v) {
		return "", fmt.Errorf("invalid version %q", v)
	}
	return v, nil
}

// IsVersion reports whether s is version-shaped by the one rule every
// consumer shares (tag ordering, provides matching, version inference).
func IsVersion(s string) bool {
	_, err := parseVersion(s)
	return err == nil
}

// semverCore and numericCore match the leading version inside what a
// package database records. semverCore spells each part as semver's
// numeric identifier, which admits no leading zero; numericCore takes any
// digit run. Both stop after three parts.
var (
	semverCore  = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.(?:0|[1-9][0-9]*)){0,2}`)
	numericCore = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){0,2}`)
	// versionEpoch is dpkg's ordering override, which is not part of
	// what upstream released.
	versionEpoch = regexp.MustCompile(`^[0-9]+:`)
)

// PackageVersion is the version a derived provide carries: the leading
// x.y.z core of what a package database records, with everything a
// distribution wrapped around it removed. Empty when the record holds no
// version this model can name, which drops the package rather than
// publishing it under the kit's own version.
//
// What comes off is packaging bookkeeping rather than a point in the
// upstream order — a Debian revision, a binNMU, a backport suffix, an apk
// release — and a consumer writing `deb/openssl >= 3.5` cannot be asked to
// know about `-1~deb13u2+dhi1`. Fewer than three parts stays as it is and
// is never padded: binutils really is 2.44, and 2.44.0 would invent
// precision the distribution never stated.
func PackageVersion(raw string) string {
	// An epoch is dpkg's ordering override, not part of what upstream
	// released: it exists to re-order a version that already shipped, so
	// carrying it would put 1:2.5.2 ahead of every 2.x that never needed
	// one.
	raw = versionEpoch.ReplaceAllString(strings.TrimSpace(raw), "")

	// A tilde in the upstream version is the one suffix that is not
	// bookkeeping: dpkg sorts it before everything, end of string
	// included, so 1.69~deb13u1 is OLDER than 1.69 and 2.0~rc1 is the
	// release candidate rather than the release. Truncating there would
	// publish the version this is not yet, and `deb/pkg >= 2.0` would
	// then be satisfied by something below 2.0.
	//
	// Nor can it be carried: §5.2 compares a non-numeric segment
	// lexically, which puts 2.0-rc1 ABOVE 2.0 and overstates it the
	// other way round. The point is unrepresentable here, so the package
	// gets no entry — which §9.6 prefers to a wrong one.
	//
	// Only in the upstream half. A tilde in the Debian revision is the
	// ordinary rebuild marker, and 9.20.26-1~deb13u1 really is 9.20.26.
	// The last hyphen is the boundary, since an upstream version may
	// carry earlier ones.
	upstream := raw
	if i := strings.LastIndex(raw, "-"); i >= 0 {
		upstream = raw[:i]
	}
	if strings.Contains(upstream, "~") {
		return ""
	}

	// The longer of the two, because a leading zero is the one place the
	// semver spelling has to give way: bc 1.07.1 is a real release, and
	// the strict pattern stops mid-version and would name it 1.0. Kept
	// whole instead, which §5.2 accepts and compares per segment, so it
	// is the same point a semver reader lands on at 1.7.1 — while
	// rewriting it that way would name a release nobody published.
	core := semverCore.FindString(raw)
	if permissive := numericCore.FindString(raw); len(permissive) > len(core) {
		core = permissive
	}
	if !IsVersion(core) {
		return ""
	}
	return core
}

// TagVersion maps a consumption-reference tag (an OCI tag, a git ref)
// onto the version it names: the bare version for a version-shaped tag,
// with git's conventional leading "v" tolerated and stripped; "" for
// everything else. The grammar itself admits no "v" — this is the one
// boundary where the outside world's spelling normalizes into the
// model's, so "v2.1.0" and "2.1.0" tags name the same version while only
// one spelling exists inside it.
func TagVersion(tag string) string {
	v := strings.TrimPrefix(tag, "v")
	if !IsVersion(v) {
		return ""
	}
	return v
}

// LatestVersion returns the highest version-shaped tag (TagVersion rules,
// so a conventional "v" prefix counts), or "" when none parses as a
// version. The returned value is the tag as spelled — it names something
// pullable — while ordering compares the normalized versions. It is the
// one tag-ordering rule shared by the resolver ("unpinned means latest")
// and the gate ("moving from X to Y"), defined here so both agree on what
// "latest" means.
func LatestVersion(tags []string) string {
	bestTag, bestVersion := "", ""
	for _, tag := range tags {
		v := TagVersion(tag)
		if v == "" {
			continue
		}
		if bestTag == "" || CompareVersions(v, bestVersion) > 0 {
			bestTag, bestVersion = tag, v
		}
	}
	return bestTag
}
