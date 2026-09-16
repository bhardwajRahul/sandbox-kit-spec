package spec

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Capability names are constrained like kit handles: lowercase alphanumeric
// plus hyphens, no leading or trailing hyphen. An optional dotted
// namespace prefix qualifies the name the way registries qualify image
// names.
var (
	capabilityName      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)
	capabilityNamespace = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
)

// DefaultCapabilityNamespace qualifies bare capability names, the way
// docker.io/library qualifies bare image names: `gh` and
// `com.docker.kit/gh` are the same capability, while a third party's
// `com.example/gh` never matches either. Deliberately a sibling of
// com.docker.sandbox, not the same namespace — com.docker.sandbox names
// contracts the RUNTIME answers (capability types), com.docker.kit names
// vocabulary KITS provide.
const DefaultCapabilityNamespace = "com.docker.kit"

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

// validCapabilityName accepts a bare name or a namespace-qualified one.
func validCapabilityName(name string) bool {
	ns, base, qualified := strings.Cut(name, "/")
	if !qualified {
		return capabilityName.MatchString(name)
	}
	return capabilityNamespace.MatchString(ns) && capabilityName.MatchString(base)
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
	if !validCapabilityName(name) {
		return Provide{}, fmt.Errorf("provides entry %q: invalid capability name %q", s, name)
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
	if !validCapabilityName(name) {
		return Require{}, fmt.Errorf("requires entry %q: invalid capability name %q", s, name)
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
