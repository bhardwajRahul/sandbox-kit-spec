package spec

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/distribution/reference"
)

// canonicalAbsPath reports whether p is absolute and in canonical form.
// Canonical form is validated rather than normalized in: /x/../skills and
// /skills are different strings for one destination, so an alias would
// evade duplicate detection and could claim a second mode for a path
// already declared.
func canonicalAbsPath(p string) bool {
	return strings.HasPrefix(p, "/") && p == path.Clean(p)
}

// Size limits for the published descriptor. OCI puts no limit on annotation
// values, but the manifest as a whole meets practical registry ceilings
// around 4 MB; the grammar exiles everything bulky, so a rich descriptor
// measures in single-digit kilobytes and these bounds exist so no kit ever
// discovers a registry's limit in production.
const (
	// SizeWarnBytes is the advisory budget: crossing it is a warning.
	SizeWarnBytes = 64 * 1024
	// SizeErrorBytes is the hard budget: crossing it fails validation.
	SizeErrorBytes = 512 * 1024
)

var (
	envVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	octalMode  = regexp.MustCompile(`^[0-7]{3,4}$`)
	sizeBytes  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?\s*([kKmMgGtT]i?[bB]?)?$`)
)

// Validate checks every rule the descriptor grammar states. It returns
// non-fatal warnings alongside the first fatal error; a nil error with
// warnings means the descriptor is usable but the author should look.
// Fatal errors carry the offending element's dotted path (FieldError), so
// consumers with the raw bytes in hand can point at source lines.
func Validate(d *Descriptor) (warnings []string, err error) {
	if d.Kind != KindWorkload && d.Kind != KindMixin && d.Kind != KindSet {
		return nil, fieldErrorf("kind", "kind must be %q, %q, or %q, got %q", KindWorkload, KindMixin, KindSet, d.Kind)
	}

	if err := validateIconURL(d.IconURL); err != nil {
		return nil, err
	}
	if err := validateCapabilityNames(d); err != nil {
		return nil, err
	}
	if err := validateCapabilityEntries(d); err != nil {
		return nil, err
	}
	if err := validateArgs(d.Args); err != nil {
		return nil, err
	}
	if err := validateRecipe(d); err != nil {
		return nil, err
	}
	if err := validateKits(d); err != nil {
		return nil, err
	}
	return warnings, nil
}

// validateIconURL holds the icon to an absolute https URL. Every other
// display field is free text a consumer decides how to show; this one
// names a resource a consumer fetches and renders, so the schemes that
// turn a rendered image into code execution or a local-file read are
// refused here rather than left to each surface to remember.
func validateIconURL(icon string) error {
	if icon == "" {
		return nil
	}
	u, err := url.Parse(icon)
	if err != nil {
		return fieldErrorf("iconUrl", "iconUrl %q: %v", icon, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return fieldErrorf("iconUrl", "iconUrl %q must be an absolute https URL", icon)
	}
	return nil
}

// validateRecipe checks the content-recipe declarations: build:,
// dockerfile:, and kits: are three homes for one thing, and an
// explicit dockerfile path must be servable by the build's dockerfile
// context — relative, and not escaping the descriptor's directory, which
// is that context's root.
func validateRecipe(d *Descriptor) error {
	if d.Build != "" && d.Dockerfile != "" {
		return fieldErrorf("dockerfile", "build: and dockerfile: are both declared; a kit's content recipe lives in exactly one place")
	}
	// A set's content is the kits it lists, so a recipe beside them is a
	// second answer to what the layers are, not an addition to them.
	if len(d.Kits) > 0 {
		if d.Build != "" {
			return fieldErrorf("build", "kits: and an inline build: block are both declared; a set's content is the kits it lists, so it declares no recipe")
		}
		if d.Dockerfile != "" {
			return fieldErrorf("dockerfile", "kits: and dockerfile: are both declared; a set's content is the kits it lists, so it declares no recipe")
		}
	}
	if d.Dockerfile == "" {
		return nil
	}
	cleaned := path.Clean(d.Dockerfile)
	if path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fieldErrorf("dockerfile", "dockerfile: %q must be a relative path inside the descriptor's directory — that directory is the build's dockerfile context, so nothing outside it can be read", d.Dockerfile)
	}
	return nil
}

// manifestDigest is the one digest spelling a listed kit may be pinned
// by. Registries address manifests by sha256 today, and accepting a
// second algorithm here would mean accepting a pin no resolver could
// compare.
var manifestDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// validateKits checks a set's kits list: a set lists at least one, the
// entries are well-formed and distinct, and their args name something.
//
// A reference is held to a shape the frontend can resolve from the
// manifest alone. A local directory or a git URL names a kit that may
// not be published at all, and a set whose content could only be
// reproduced from someone's working copy is not shareable, which is the
// whole point of writing one.
func validateKits(d *Descriptor) error {
	if d.Kind == KindSet && len(d.Kits) == 0 {
		return fieldErrorf("kits", "kind: %s lists no kits; a set's content is the kits it lists", KindSet)
	}
	// Written and left empty, under any kind. Absent means a kit with
	// an ordinary recipe; present and empty means an author who meant
	// to list something, and reading it as absence would publish a
	// declaration-only kit where content was intended. The schema
	// refuses it through minItems, so the two agree.
	if d.Kits != nil && len(d.Kits) == 0 {
		return fieldErrorf("kits", "kits: is present but empty; list the kits this one is merged from, or drop the field")
	}
	seen := map[string]int{}
	for i, k := range d.Kits {
		at := fmt.Sprintf("kits[%d]", i)
		if strings.TrimSpace(k.Ref) == "" {
			return fieldErrorf(at+".ref", "kits[%d]: ref is required — the reference this kit is consumed by", i)
		}
		// The digest is judged whatever the reference looks like: it is
		// a pin the author wrote literally, and nothing an arg resolves
		// to can make a malformed one well-formed.
		if k.Digest != "" && !manifestDigest.MatchString(k.Digest) {
			return fieldErrorf(at+".digest", "kits[%d]: digest %q is not a sha256 manifest digest", i, k.Digest)
		}
		// Only the reference grammar defers, and only for an arg that
		// resolves in time to be of use. A set's kits are resolved
		// during the build, so a build-phase arg — the registry
		// namespace a set of one org's kits is published under is
		// exactly that — leaves a literal reference behind before
		// anything needs it. A create-phase arg does not: expansion
		// would leave the placeholder standing and the build would
		// fail trying to parse it as an image reference, which is a
		// worse place to learn it than here.
		//
		// An undeclared name is left alone: ValidateRaw reports it
		// against the whole document, and saying it twice differently
		// would only confuse the author.
		if ContainsArgRef(k.Ref) {
			for _, name := range ReferencedArgs([]byte(k.Ref)) {
				if decl, declared := d.Args[name]; declared && decl.BuildArg == "" {
					return fieldErrorf(at+".ref", "kits[%d]: ref names %q, which resolves at create, but a set's kits are resolved at build; declare it with buildArg:, or write the reference out", i, name)
				}
			}
		} else if err := validateKitReference(at, i, k); err != nil {
			return err
		}
		if prev, dup := seen[k.Ref]; dup {
			return fieldErrorf(at+".ref", "kits[%d]: %q already listed at kits[%d]", i, k.Ref, prev)
		}
		seen[k.Ref] = i
		for name := range k.Args {
			if !envVarName.MatchString(name) {
				return fieldErrorf(at+".args", "kits[%d]: %q is not a valid arg name", i, name)
			}
		}
	}
	return nil
}

// validateKitReference holds one listed kit's reference to the shape a
// frontend can resolve.
//
// The two refusals come first because they name what an author is
// likely to have reached for, and a generic parse error would not say
// why those forms cannot work. Everything else is held to the image
// reference grammar itself rather than to a set of prefix rules: a
// descriptor that validates has to be one the frontend can resolve, and
// anything short of the real parser leaves shapes (a URL with a scheme,
// an uppercase path) that pass here and fail at build.
func validateKitReference(at string, i int, k Kit) error {
	if k.Ref != strings.TrimSpace(k.Ref) || strings.ContainsAny(k.Ref, " \t\n") {
		return fieldErrorf(at+".ref", "kits[%d]: ref %q contains whitespace", i, k.Ref)
	}
	if strings.HasPrefix(k.Ref, "git+") {
		return fieldErrorf(at+".ref", "kits[%d]: ref %q is a git reference; a set's kits are published kit images, so the frontend can resolve them from the manifest alone", i, k.Ref)
	}
	if strings.HasPrefix(k.Ref, ".") || strings.HasPrefix(k.Ref, "/") {
		return fieldErrorf(at+".ref", "kits[%d]: ref %q is a local path; a set's kits are published kit images, so the set stays reproducible away from the directory it was written in", i, k.Ref)
	}
	named, err := reference.ParseNormalizedNamed(k.Ref)
	if err != nil {
		return fieldErrorf(at+".ref", "kits[%d]: ref %q is not an image reference: %v", i, k.Ref, err)
	}
	// A reference that already carries a digest is pinned twice once
	// digest: is also stated, and two pins that disagree cannot both be
	// what the author meant. Judged here rather than only where the
	// build resolves it, because an artifact this frontend never built
	// reaches consumers through ValidatePublished alone — and a
	// descriptor recording one pin beside content fetched by another
	// is provenance that cannot be checked.
	if canonical, ok := named.(reference.Canonical); ok && k.Digest != "" && canonical.Digest().String() != k.Digest {
		return fieldErrorf(at+".digest", "kits[%d]: ref pins %s but digest: says %s; one kit has one pin", i, canonical.Digest(), k.Digest)
	}
	return nil
}

// ValidateRaw runs Validate plus the checks that need the raw bytes: the
// size budget, and that every ${{ kit.args.* }} reference names a declared
// arg.
func ValidateRaw(raw []byte, d *Descriptor) (warnings []string, err error) {
	warnings, err = Validate(d)
	if err != nil {
		return nil, err
	}

	if len(raw) > SizeErrorBytes {
		return nil, fmt.Errorf("descriptor is %d bytes, over the %d byte budget; move bulk into layers", len(raw), SizeErrorBytes)
	}
	if len(raw) > SizeWarnBytes {
		warnings = append(warnings, fmt.Sprintf("descriptor is %d bytes; the advisory budget is %d", len(raw), SizeWarnBytes))
	}

	for _, name := range ReferencedArgs(raw) {
		if _, ok := d.Args[name]; !ok {
			return nil, fmt.Errorf("descriptor references ${{ kit.args.%s }} but declares no arg %q", name, name)
		}
	}
	return warnings, nil
}

func validateCapabilityNames(d *Descriptor) error {
	// An authored version may reference a build-phase arg, expanded into
	// the published descriptor; ValidatePublished requires the literal.
	if d.Version != "" && !argRef.MatchString(d.Version) {
		if _, err := parseVersion(d.Version); err != nil {
			return fieldErrorf("version", "version: %v", err)
		}
	}
	for i, s := range d.Provides {
		// An authored provide may reference a build-phase arg
		// (`gh@${{ kit.args.version }}`); it parses only after the
		// frontend expands it into the published descriptor, which
		// ValidatePublished enforces.
		if argRef.MatchString(s) {
			continue
		}
		if _, err := ParseProvide(s); err != nil {
			return fieldErrorf(fmt.Sprintf("provides[%d]", i), "%v", err)
		}
	}
	// Requires, integrates, and conflicts stay literal in both forms: they
	// are what the resolver judges, and a parameterized constraint would
	// make the judgment depend on caller input.
	for i, s := range d.Requires {
		if argRef.MatchString(s) {
			return fieldErrorf(fmt.Sprintf("requires[%d]", i), "requires entry %q: arg references are not allowed in requires", s)
		}
		if _, err := ParseRequire(s); err != nil {
			return fieldErrorf(fmt.Sprintf("requires[%d]", i), "%v", err)
		}
	}
	for i, s := range d.Integrates {
		if argRef.MatchString(s) {
			return fieldErrorf(fmt.Sprintf("integrates[%d]", i), "integrates entry %q: arg references are not allowed in integrates", s)
		}
		if _, err := ParseRequire(s); err != nil {
			return fieldErrorf(fmt.Sprintf("integrates[%d]", i), "%v", err)
		}
	}
	for i, s := range d.Conflicts {
		if argRef.MatchString(s) {
			return fieldErrorf(fmt.Sprintf("conflicts[%d]", i), "conflicts entry %q: arg references are not allowed in conflicts", s)
		}
		if !validCapabilityName(s) {
			return fieldErrorf(fmt.Sprintf("conflicts[%d]", i), "conflicts entry %q: invalid capability name", s)
		}
	}
	return nil
}

// ValidatePublished runs ValidateRaw plus the published-form requirement:
// every build-phase reference has been expanded, so provides entries are
// literal. Create-phase references in hooks and files legitimately remain.
func ValidatePublished(raw []byte, d *Descriptor) (warnings []string, err error) {
	warnings, err = ValidateRaw(raw, d)
	if err != nil {
		return nil, err
	}
	if argRef.MatchString(d.Version) {
		return nil, fieldErrorf("version", "published descriptor still references an arg in version %q; build-phase expansion did not run", d.Version)
	}
	for i, s := range d.Provides {
		if argRef.MatchString(s) {
			return nil, fieldErrorf(fmt.Sprintf("provides[%d]", i), "published descriptor still references an arg in provides entry %q; build-phase expansion did not run", s)
		}
		if _, err := ParseProvide(s); err != nil {
			return nil, fieldErrorf(fmt.Sprintf("provides[%d]", i), "%v", err)
		}
	}
	// §9.2 is a property of every published descriptor, not only of the
	// frontend's build path, so third-party artifacts are held to it too.
	if err := RequireVersionedProvides(d); err != nil {
		return nil, err
	}
	// Build-phase args are baked before signing, so a reference to one
	// anywhere in the published form means expansion did not reach it.
	// Checked across the whole document rather than field by field:
	// create-phase references legitimately remain in hooks and file
	// contents, and only the arg's own declaration says which kind a
	// reference is.
	for _, name := range ReferencedArgs(raw) {
		if decl, declared := d.Args[name]; declared && decl.BuildArg != "" {
			return nil, fieldErrorf("args."+name, "published descriptor still references ${{ kit.args.%s }}, which resolves at build; expansion did not reach it", name)
		}
	}
	// kind: set is an authoring convenience the frontend resolves into
	// the kind the listed kits imply. Reaching a consumer means the
	// merge never ran, so the layers are not what the descriptor
	// describes.
	if d.Kind == KindSet {
		return nil, fieldErrorf("kind", "published descriptor still declares kind: %s; the frontend derives %q or %q from the kits it lists, so this artifact was never merged", KindSet, KindWorkload, KindMixin)
	}
	// An authored entry names a version by tag; a published one has
	// been resolved, and the pin is what makes the record of how the
	// content was produced reproducible rather than a moving claim.
	for i, k := range d.Kits {
		if argRef.MatchString(k.Ref) {
			return nil, fieldErrorf(fmt.Sprintf("kits[%d].ref", i), "published descriptor still references an arg in %q; a set's kits resolve at build, so build-phase expansion did not run", k.Ref)
		}
		if k.Digest == "" {
			return nil, fieldErrorf(fmt.Sprintf("kits[%d].digest", i), "published descriptor lists %s with no digest; the frontend pins every one of a set's kits to the manifest it resolved", k.Ref)
		}
	}
	return warnings, nil
}

// RequireVersionedProvides is the publish-time rule the frontend enforces
// on the expanded descriptor: every provides entry must carry a version —
// its own @version, or the descriptor's version: fallback. A published kit
// with an unversioned provide would satisfy only unconstrained requires and
// silently defeat version-constraint resolution; the build is where the author
// can still fix it. Consumption references (a version tag) can override
// these versions, never substitute for them. Kits with no provides publish
// fine: they offer nothing matchable, so there is nothing to version.
func RequireVersionedProvides(d *Descriptor) error {
	if d.Version != "" {
		return nil
	}
	for i, s := range d.Provides {
		p, err := ParseProvide(s)
		if err != nil {
			return fieldErrorf(fmt.Sprintf("provides[%d]", i), "%v", err)
		}
		if p.Version == "" {
			return fieldErrorf(fmt.Sprintf("provides[%d]", i),
				"provides entry %q has no version: add @<version> here or a top-level version: field (a version-shaped image tag can override it at consumption, but a published kit must carry one)", s)
		}
	}
	return nil
}

// RequireAuthoredProvides refuses a provides entry an author must not
// write: §5.1 reserves the deb/ and apk/ namespaces for §9.6, where
// publishing fills them from the package databases in the content.
//
// Authored form only, which is why it is not folded into Validate: the
// published form legitimately carries these, and a runtime revalidating
// a descriptor on load has to accept what publishing put there. The
// distinction is the whole point — an entry under these namespaces is
// something read off a filesystem, and one written by hand would assert
// a fact about content instead of offering a capability, with nothing
// left to catch the difference.
func RequireAuthoredProvides(d *Descriptor) error {
	for i, s := range d.Provides {
		p, err := ParseProvide(s)
		// A malformed entry, or one still holding an arg reference, is
		// another rule's to report; a name that cannot be parsed cannot
		// be in a reserved namespace either.
		if err != nil {
			continue
		}
		if IsDerivedProvide(p.Name) {
			namespace, _, _ := strings.Cut(p.Name, "/")
			return fieldErrorf(fmt.Sprintf("provides[%d]", i),
				"provides entry %q is in the %s/ namespace, which publishing fills from the image's package database; drop it and let the build state what the content carries", s, namespace)
		}
	}
	return nil
}

// needType is <namespace>/<name>@<version>: a dotted lowercase
// namespace, a hyphenated lowercase name, an integer config-schema
// version.
var needType = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?/[a-z0-9]([a-z0-9-]*[a-z0-9])?@[1-9][0-9]*$`)

// singletonCapabilities are policy-shaped types: at most one entry each.
// Instance-shaped types (credential, volume, port, usb-device) appear
// once per thing requested and dedup on their own key; unknown types
// dedup on the exact request (type + config).
var singletonCapabilities = map[string]bool{
	CapabilityNetworkPolicy:   true,
	CapabilityNetworkPolicyV2: true,
	CapabilityResources:       true,
	CapabilityPrivileged:      true,
	CapabilityKitRegistry:     true,
	CapabilityAgentSessions:   true,
	CapabilityLifecycle:       true,
	CapabilityAgentContext:    true,
	CapabilitySbx:             true,
}

// argvContains reports whether any argv element contains the substring
// — placeholders may ride inside a larger token ("--prompt={{.Prompt}}").
func argvContains(argv []string, sub string) bool {
	for _, a := range argv {
		if strings.Contains(a, sub) {
			return true
		}
	}
	return false
}

// configlessCapabilities take no config at all: present or absent.
var configlessCapabilities = map[string]bool{
	CapabilityPrivileged:  true,
	CapabilityKitRegistry: true,
	CapabilitySbx:         true,
}

// validateCapabilityEntries checks the typed capability list. Type syntax and
// arity hold for every entry; config schemas are enforced strictly for
// types this grammar knows — an unknown type is the extension point
// working as designed, and the HOST decides at resolve whether it can
// provide it (fail-closed when required, skipped when optional). The
// one cross-entry invariant follows the loop: every credential inject
// domain must appear in the matching phase of the network policy's
// allow list — injection sets the header, egress is gated separately,
// and the two must agree per phase or a credential is presented to a
// domain the phase cannot reach.
func validateCapabilityEntries(d *Descriptor) error {
	needs := d.Capabilities
	// The platform contract is about the image config a workload's layers
	// come with, and a mixin's config never becomes the composed image's.
	// Rejected here rather than only in the kit suite because Merge
	// unions a set's declarations into one workload-kinded descriptor:
	// by the time an artifact is judged, a mixin's claim is
	// indistinguishable from the workload's own. A set is exempt — its
	// kind is derived from its members later.
	if d.Kind == KindMixin && HasCapability(needs, CapabilitySbx) {
		return fieldErrorf("capabilities", "%s is workload-only: a mixin's image config never becomes the composed image's, so the identity it would promise is not the one a host reads", CapabilitySbx)
	}
	seenSingleton := map[string]int{}
	seenExact := map[string]int{}
	seenCredential := map[string]int{}
	seenVolume := map[string]int{}
	seenSkills := map[string]int{}
	seenPort := map[string]int{}

	deferCrossChecks := false
	for i, n := range needs {
		path := fmt.Sprintf("capabilities[%d]", i)
		if !needType.MatchString(n.Type) {
			return fieldErrorf(path+".type", "capabilities[%d]: type %q is not <namespace>/<name>@<version>", i, n.Type)
		}
		if singletonCapabilities[n.Type] {
			if prev, dup := seenSingleton[n.Type]; dup {
				return fieldErrorf(path+".type", "capabilities[%d]: %s already declared at capabilities[%d]", i, n.Type, prev)
			}
			seenSingleton[n.Type] = i
		}
		if key := capabilitySurfaceEntry(n); true {
			if prev, dup := seenExact[key]; dup {
				return fieldErrorf(path, "capabilities[%d]: identical to capabilities[%d]", i, prev)
			}
			seenExact[key] = i
		}
		// Presence, not emptiness: `config: {}` and `config: null` are
		// config values, which these types' schemas (`not: {}`) reject,
		// and neither is distinguishable from an omitted key by the
		// decoded map alone. Testing length or nil-ness would let one
		// descriptor pass here and fail schema validation.
		if configlessCapabilities[n.Type] && n.ConfigStated() {
			return fieldErrorf(path+".config", "capabilities[%d]: %s takes no config", i, n.Type)
		}

		// A parameterized entry — its config references a kit arg — defers
		// its typed validation to ValidateEffective, after expansion has
		// resolved every placeholder: a string placeholder cannot pass a
		// numeric field's decode, and a placeholder value cannot pass a
		// value check. Type grammar, arity, and dedup above still hold;
		// what the entry ASKS never goes unchecked, only WHEN it is
		// checked moves.
		if capabilityIsParameterized(n) {
			deferCrossChecks = true
			// Except for the rules about which fields are present:
			// those do not wait for values, and deferring them let a
			// contradictory pair through to a merge that normalizes
			// one of the two away — after which the evidence is gone
			// and the strict pass at the end sees a valid descriptor.
			if err := validatePresenceRules(path, i, n); err != nil {
				return err
			}
			continue
		}

		switch n.Type {
		case CapabilityNetworkPolicy:
			var p PhasedNetwork
			if err := DecodeCapabilityConfig(n, &p); err != nil {
				return fieldErrorf(path+".config", "capabilities[%d]: %v", i, err)
			}
		case CapabilityNetworkPolicyV2:
			var p PhasedNetworkV2
			if err := DecodeCapabilityConfig(n, &p); err != nil {
				return fieldErrorf(path+".config", "capabilities[%d]: %v", i, err)
			}
			if err := validateNetworkEntries(path, i, &p); err != nil {
				return err
			}
		case CapabilityCredential:
			c, err := validateCredentialNeed(path, i, n)
			if err != nil {
				return err
			}
			key := c.Service + "\x00" + c.Phase
			if prev, dup := seenCredential[key]; dup {
				return fieldErrorf(path+".config.service", "capabilities[%d]: credential for service %q phase %q already declared at capabilities[%d]", i, c.Service, c.Phase, prev)
			}
			seenCredential[key] = i
		case CapabilityVolume:
			var v Volume
			if err := DecodeCapabilityConfig(n, &v); err != nil {
				return fieldErrorf(path+".config", "capabilities[%d]: %v", i, err)
			}
			if !strings.HasPrefix(v.Path, "/") {
				return fieldErrorf(path+".config.path", "capabilities[%d]: volume path %q must be absolute", i, v.Path)
			}
			if v.Size != "" && !sizeBytes.MatchString(v.Size) {
				return fieldErrorf(path+".config.size", "capabilities[%d]: invalid size %q", i, v.Size)
			}
			if v.Mode != "" && !octalMode.MatchString(v.Mode) {
				return fieldErrorf(path+".config.mode", "capabilities[%d]: invalid octal mode %q", i, v.Mode)
			}
			if prev, dup := seenVolume[v.Path]; dup {
				return fieldErrorf(path+".config.path", "capabilities[%d]: volume for %q already declared at capabilities[%d]", i, v.Path, prev)
			}
			seenVolume[v.Path] = i
		case CapabilityAgentSkills:
			var s AgentSkills
			if err := DecodeCapabilityConfig(n, &s); err != nil {
				return fieldErrorf(path+".config", "capabilities[%d]: %v", i, err)
			}
			// Canonical form is required, not normalized in: /x/../skills
			// and /skills are different keys for one mount destination, so
			// an alias would slip past the duplicate check below and could
			// declare a second mode for the same path.
			if s.Path == "/" || !canonicalAbsPath(s.Path) {
				return fieldErrorf(path+".config.path", "capabilities[%d]: skills path %q must be absolute and canonical (no . or .. segments, no trailing slash, not the root)", i, s.Path)
			}
			switch s.Mode {
			case "", SkillsReadOnly, SkillsReadWrite:
			default:
				return fieldErrorf(path+".config.mode", "capabilities[%d]: mode must be %q or %q, got %q", i, SkillsReadOnly, SkillsReadWrite, s.Mode)
			}
			// No dedup by path here: two entries naming one path with the
			// same mode are identical requests, which the exact-duplicate
			// rule above already rejects; naming one path with different
			// modes is contradictory, so the narrower one is the request
			// and stating both is an error.
			if prev, dup := seenSkills[s.Path]; dup {
				return fieldErrorf(path+".config.path", "capabilities[%d]: skills for %q already declared at capabilities[%d]", i, s.Path, prev)
			}
			seenSkills[s.Path] = i
		case CapabilityPort:
			var p Port
			if err := DecodeCapabilityConfig(n, &p); err != nil {
				return fieldErrorf(path+".config", "capabilities[%d]: %v", i, err)
			}
			if p.Container < 1 || p.Container > 65535 {
				return fieldErrorf(path+".config.container", "capabilities[%d]: container port %d out of range", i, p.Container)
			}
			switch p.Transport {
			case "", "tcp", "udp":
			default:
				return fieldErrorf(path+".config.transport", "capabilities[%d]: transport must be tcp or udp, got %q", i, p.Transport)
			}
			key := PortKey(p)
			if prev, dup := seenPort[key]; dup {
				return fieldErrorf(path+".config.container", "capabilities[%d]: port %d already declared at capabilities[%d]", i, p.Container, prev)
			}
			seenPort[key] = i
		case CapabilityUSBDevice:
			var u USBDevice
			if err := DecodeCapabilityConfig(n, &u); err != nil {
				return fieldErrorf(path+".config", "capabilities[%d]: %v", i, err)
			}
			hasID := u.VendorID != "" || u.ProductID != ""
			if hasID == (u.Class != "") {
				return fieldErrorf(path+".config", "capabilities[%d]: declare either vendorId/productId or class, not both and not neither", i)
			}
			if (u.VendorID != "") != (u.ProductID != "") {
				return fieldErrorf(path+".config", "capabilities[%d]: vendorId and productId go together", i)
			}
		case CapabilityResources:
			var r Resources
			if err := DecodeCapabilityConfig(n, &r); err != nil {
				return fieldErrorf(path+".config", "capabilities[%d]: %v", i, err)
			}
			if r.CPU < 0 {
				return fieldErrorf(path+".config.cpu", "capabilities[%d]: cpu must be >= 0", i)
			}
			if r.Memory != "" && !sizeBytes.MatchString(r.Memory) {
				return fieldErrorf(path+".config.memory", "capabilities[%d]: invalid memory %q", i, r.Memory)
			}
		case CapabilityAgentSessions:
			var a AgentSessions
			if err := DecodeCapabilityConfig(n, &a); err != nil {
				return fieldErrorf(path+".config", "capabilities[%d]: %v", i, err)
			}
			if len(a.Prompt) == 0 && len(a.Resume) == 0 && len(a.Continue) == 0 && len(a.List) == 0 {
				return fieldErrorf(path+".config", "capabilities[%d]: agent-sessions declares no verbs; drop the entry instead", i)
			}
			// The placeholder is the verb's whole point: a prompt verb
			// that never receives the prompt (or a resume that never
			// names the session) runs the agent with the caller's input
			// silently discarded.
			if len(a.Prompt) > 0 && !argvContains(a.Prompt, SessionPromptPlaceholder) {
				return fieldErrorf(path+".config.prompt", "capabilities[%d]: prompt must reference %s", i, SessionPromptPlaceholder)
			}
			if len(a.Resume) > 0 && !argvContains(a.Resume, SessionIDPlaceholder) {
				return fieldErrorf(path+".config.resume", "capabilities[%d]: resume must reference %s", i, SessionIDPlaceholder)
			}
		case CapabilityLifecycle:
			var l Lifecycle
			if err := DecodeCapabilityConfig(n, &l); err != nil {
				return fieldErrorf(path+".config", "capabilities[%d]: %v", i, err)
			}
			if len(l.Install) == 0 && len(l.Startup) == 0 && len(l.Files) == 0 && len(l.Interactive) == 0 {
				return fieldErrorf(path+".config", "capabilities[%d]: lifecycle declares no hooks, no files, and no interactive tail; drop the entry instead", i)
			}
			if err := validateLifecycle(path, i, &l); err != nil {
				return err
			}
		case CapabilityAgentContext:
			var a AgentContext
			if err := DecodeCapabilityConfig(n, &a); err != nil {
				return fieldErrorf(path+".config", "capabilities[%d]: %v", i, err)
			}
			// A set may state the profile when the kits it lists make
			// it a workload; the merged descriptor carries the derived
			// kind, so a set of mixins claiming one is caught there.
			if a.Filename != "" && d.Kind != KindWorkload && d.Kind != KindSet {
				return fieldErrorf(path+".config.filename", "capabilities[%d]: agent-context filename is workload-kit-only: the profile belongs to the kit that owns the environment; a mixin contributes contentFile or content", i)
			}
			if a.ContentFile != "" && a.Content != "" {
				return fieldErrorf(path+".config", "capabilities[%d]: agent-context contentFile and content are mutually exclusive", i)
			}
		}
	}

	one, hasOne := seenSingleton[CapabilityNetworkPolicy]
	two, hasTwo := seenSingleton[CapabilityNetworkPolicyV2]
	if hasOne && hasTwo {
		return fieldErrorf(fmt.Sprintf("capabilities[%d].type", two),
			"capabilities[%d]: %s cannot be declared beside %s at capabilities[%d]; a descriptor states one network-policy version",
			two, CapabilityNetworkPolicyV2, CapabilityNetworkPolicy, one)
	}

	// The inject⊆allow invariant needs literal domains on both sides;
	// with any parameterized entry in play it runs on the effective
	// descriptor instead, where every domain is literal.
	if deferCrossChecks {
		return nil
	}
	return validateInjectWithinAllow(needs)
}

// httpMethods are the tokens a NetworkEntry may name. Canonical uppercase is
// required rather than normalized in, so a published rule reads the same
// as the one enforcement matches.
var httpMethods = map[string]bool{
	"GET":     true,
	"HEAD":    true,
	"POST":    true,
	"PUT":     true,
	"PATCH":   true,
	"DELETE":  true,
	"OPTIONS": true,
	"TRACE":   true,
	"CONNECT": true,
}

// HTTPMethods returns the method tokens a NetworkEntry may name, excluding
// MethodAny, sorted. The published schema's enum is pinned against it.
func HTTPMethods() []string {
	out := make([]string, 0, len(httpMethods))
	for m := range httpMethods {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// validateNetworkEntries checks both phases' entries.
//
// A bounded entry names its hosts exactly. A pattern cannot be bounded
// and unbounded at once — an entry bounding "*.example.com" to GET would
// overlap any other entry naming a host inside it, and nothing here can
// rank the two, because the matcher deciding what a pattern covers is
// runtime-owned. The restriction is per-entry rather than per-phase: an
// unbounded entry beside a bounded one keeps @1's patterns, because it
// raises no question of rank with anything.
//
// Deny entries may name patterns however they are bounded: a deny wins
// outright, so an overlap between two of them decides the same way.
func validateNetworkEntries(path string, i int, p *PhasedNetworkV2) error {
	for _, phase := range []struct {
		name  string
		rules *NetworkRulesV2
	}{{"install", p.Install}, {"runtime", p.Runtime}} {
		if phase.rules == nil {
			continue
		}
		for j, e := range phase.rules.Allow {
			at := fmt.Sprintf("%s.config.%s.allow[%d]", path, phase.name, j)
			if err := validateNetworkEntry(at, i, phase.name, e); err != nil {
				return err
			}
			if !e.Bounded() {
				continue
			}
			for _, h := range e.Hosts {
				if isHostPattern(h) {
					return fieldErrorf(at+".hosts",
						"capabilities[%d]: %s allow entry bounds the pattern %q; an entry stating methods or paths names its hosts exactly, or the pattern would be bounded and unbounded at once",
						i, phase.name, h)
				}
			}
		}
		for j, e := range phase.rules.Deny {
			at := fmt.Sprintf("%s.config.%s.deny[%d]", path, phase.name, j)
			if err := validateNetworkEntry(at, i, phase.name, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// isHostPattern reports whether an entry is a glob rather than one
// literal host.
func isHostPattern(h string) bool { return strings.Contains(h, "*") }

// validateNetworkEntryShape judges which fields an entry states and
// how many things they hold — the part of a policy a placeholder
// cannot hide, and the part that decides how wide the entry is.
//
// Held to even while the text is still parameterized, because the
// merge decodes and re-encodes what it is given: an entry stating
// methods: [] is invalid, and deferring the rule would let omitempty
// drop the field and publish the unbounded allow that omission means.
func validateNetworkEntryShape(path string, i int, phase string, r NetworkEntry) error {
	if len(r.Hosts) == 0 {
		return fieldErrorf(path+".hosts", "capabilities[%d]: %s entry declares no hosts", i, phase)
	}
	for _, h := range r.Hosts {
		if h == "" {
			return fieldErrorf(path+".hosts", "capabilities[%d]: %s entry has an empty host", i, phase)
		}
	}
	// Wildcard semantics belong to OMITTED fields alone: a generated
	// document stating methods: [] would otherwise silently grant every
	// method, which is the opposite of what an empty restriction reads as.
	if r.Methods != nil && len(r.Methods) == 0 {
		// Omitting methods only unbounds the entry when nothing else
		// bounds it; with paths stated it fails the paths-without-methods
		// rule below, so the remedy has to name both.
		remedy := "omit the field to leave the entry unbounded, or name the methods"
		if len(r.Paths) > 0 {
			remedy = "name the methods the paths bound, or omit paths too to leave the entry unbounded"
		}
		return fieldErrorf(path+".methods",
			"capabilities[%d]: %s entry states an empty methods list; %s", i, phase, remedy)
	}
	if r.Paths != nil && len(r.Paths) == 0 {
		return fieldErrorf(path+".paths",
			"capabilities[%d]: %s entry states an empty paths list; omit the field for every path, or name the paths", i, phase)
	}
	if len(r.Paths) > 0 && len(r.Methods) == 0 {
		return fieldErrorf(path+".paths",
			"capabilities[%d]: %s entry states paths without methods; name the methods the paths bound, or %s for every method",
			i, phase, "methods: ["+MethodAny+"]")
	}
	return nil
}

func validateNetworkEntry(path string, i int, phase string, r NetworkEntry) error {
	if err := validateNetworkEntryShape(path, i, phase, r); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range r.Methods {
		if m != MethodAny && !httpMethods[m] {
			return fieldErrorf(path+".methods",
				"capabilities[%d]: %s entry method %q is not an uppercase HTTP method or %s",
				i, phase, m, MethodAny)
		}
		if seen[m] {
			return fieldErrorf(path+".methods", "capabilities[%d]: %s entry repeats method %q", i, phase, m)
		}
		seen[m] = true
	}
	if seen[MethodAny] && len(r.Methods) > 1 {
		return fieldErrorf(path+".methods",
			"capabilities[%d]: %s entry names %s beside a specific method; %s already covers every method",
			i, phase, MethodAny, MethodAny)
	}
	for _, p := range r.Paths {
		if !strings.HasPrefix(p, "/") {
			return fieldErrorf(path+".paths", "capabilities[%d]: %s entry path %q must start with \"/\"", i, phase, p)
		}
	}
	return nil
}

// validatePresenceRules judges what an entry states rather than what
// it states it as, which is the part of a config a placeholder does
// not hide.
func validatePresenceRules(path string, i int, n Capability) error {
	if n.Type == CapabilityNetworkPolicy {
		var p PhasedNetworkV2
		if err := DecodeCapabilityConfig(n, &p); err != nil {
			// Undecodable is the merge's to report, in the words it
			// has for a re-export it cannot carry.
			return nil
		}
		for _, phase := range []struct {
			name  string
			rules *NetworkRulesV2
		}{{"install", p.Install}, {"runtime", p.Runtime}} {
			if phase.rules == nil {
				continue
			}
			for _, group := range []struct {
				name    string
				entries []NetworkEntry
			}{{"allow", phase.rules.Allow}, {"deny", phase.rules.Deny}} {
				for j, e := range group.entries {
					at := fmt.Sprintf("%s.config.%s.%s[%d]", path, phase.name, group.name, j)
					if err := validateNetworkEntryShape(at, i, phase.name, e); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if n.Type != CapabilityAgentContext {
		return nil
	}
	// Read as raw keys: decoding wants types the placeholder does not
	// have, and presence is all this rule asks about.
	_, hasFile := n.Config["contentFile"]
	_, hasContent := n.Config["content"]
	if hasFile && hasContent {
		return fieldErrorf(path+".config", "capabilities[%d]: agent-context contentFile and content are mutually exclusive", i)
	}
	return nil
}

// capabilityIsParameterized reports whether the entry's config references
// a kit arg anywhere in its (canonical-JSON) rendering.
func capabilityIsParameterized(n Capability) bool {
	if len(n.Config) == 0 {
		return false
	}
	data, err := json.Marshal(n.Config)
	if err != nil {
		return false
	}
	return ContainsArgRef(string(data))
}

// validateCredentialNeed decodes and checks one credential entry.
func validateCredentialNeed(path string, i int, n Capability) (*Credential, error) {
	var c Credential
	if err := DecodeCapabilityConfig(n, &c); err != nil {
		return nil, fieldErrorf(path+".config", "capabilities[%d]: %v", i, err)
	}
	if c.Service == "" {
		return nil, fieldErrorf(path+".config.service", "capabilities[%d]: credential service is required", i)
	}
	if !handleName.MatchString(c.Service) {
		return nil, fieldErrorf(path+".config.service", "capabilities[%d]: invalid service name %q", i, c.Service)
	}
	if c.Phase != "install" && c.Phase != "runtime" {
		return nil, fieldErrorf(path+".config.phase", "capabilities[%d] (%s): phase must be \"install\" or \"runtime\", got %q", i, c.Service, c.Phase)
	}
	if c.APIKey == nil && c.OAuth == nil {
		return nil, fieldErrorf(path+".config", "capabilities[%d] (%s): declare apiKey or oauth", i, c.Service)
	}
	// Present nulls decay through typed decoding — to nil pointers, empty
	// strings, or nil maps — and would masquerade as omitted fields,
	// while the schema rejects null at every credential position. One
	// walk judges the whole raw shape; the single subtree where null is
	// legal is credentialFile.structure under the json encoding, whose
	// values the schema leaves open (the toml walk judges them
	// separately).
	if at, bad := firstNullInConfig("config", n.Config, "config.oauth.credentialFile.structure"); bad {
		return nil, fieldErrorf(path+"."+at, "capabilities[%d] (%s): %s is null; omit the field or declare a value", i, c.Service, strings.TrimPrefix(at, "config."))
	}
	if c.APIKey != nil {
		// An empty name with inject rules is the inject-only shape: the
		// key exists solely as outbound rewrites, with no environment
		// presence — not even a sentinel.
		if c.APIKey.Name == "" && len(c.APIKey.Inject) == 0 {
			return nil, fieldErrorf(path+".config.apiKey", "capabilities[%d] (%s): apiKey needs a name, inject rules, or both", i, c.Service)
		}
		if c.APIKey.Name != "" && !envVarName.MatchString(c.APIKey.Name) {
			return nil, fieldErrorf(path+".config.apiKey.name", "capabilities[%d] (%s): apiKey.name %q is not a valid env var name", i, c.Service, c.APIKey.Name)
		}
		for j, inj := range c.APIKey.Inject {
			if inj.Domain == "" {
				return nil, fieldErrorf(fmt.Sprintf("%s.config.apiKey.inject[%d]", path, j), "capabilities[%d] (%s): inject[%d]: domain is required", i, c.Service, j)
			}
		}
	}
	if c.OAuth != nil && c.OAuth.TokenEndpoint != nil && c.OAuth.TokenEndpoint.Host == "" {
		return nil, fieldErrorf(path+".config.oauth.tokenEndpoint", "capabilities[%d] (%s): oauth.tokenEndpoint.host is required", i, c.Service)
	}
	if c.OAuth != nil && c.OAuth.CredentialFile != nil {
		// An explicitly empty format decays to the same "" as an omitted
		// one through typed decoding, but the published schema's enum
		// rejects it — one grammar, so the raw shape is judged first.
		// (Present nulls are already rejected by the config-wide walk.)
		if oauth, ok := n.Config["oauth"].(map[string]any); ok {
			if cf, ok := oauth["credentialFile"].(map[string]any); ok {
				if v, present := cf["format"]; present && v == "" {
					return nil, fieldErrorf(path+".config.oauth.credentialFile.format", "capabilities[%d] (%s): credentialFile.format is empty; omit the field for json or name an encoding", i, c.Service)
				}
			}
		}
		switch c.OAuth.CredentialFile.Format {
		case "", "json", "toml":
		default:
			return nil, fieldErrorf(path+".config.oauth.credentialFile.format", "capabilities[%d] (%s): credentialFile.format %q is not json or toml", i, c.Service, c.OAuth.CredentialFile.Format)
		}
		// An omitted structure is schema-legal (path alone is required),
		// so there is nothing to walk; the null check is for values
		// inside a structure that exists.
		if c.OAuth.CredentialFile.Format == "toml" && c.OAuth.CredentialFile.Structure != nil {
			if at, bad := firstNonTOMLValue("structure", c.OAuth.CredentialFile.Structure); bad {
				return nil, fieldErrorf(path+".config.oauth.credentialFile."+at, "capabilities[%d] (%s): %s is null, which TOML cannot represent", i, c.Service, at)
			}
		}
	}
	return &c, nil
}

// firstNullInConfig walks a raw capability config for present null
// values, which typed decoding erases into omitted-looking zero values
// while the schema rejects them. Skip names one subtree (dot-joined path)
// where null is legal. Keys are sorted so the diagnostic is
// deterministic.
func firstNullInConfig(at string, v any, skip string) (string, bool) {
	switch t := v.(type) {
	case nil:
		// A null AT the skip path is still a null (structure: null is
		// not a structure); only values inside the subtree are open.
		return at, true
	case map[string]any:
		if at == skip {
			return "", false
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if deep, bad := firstNullInConfig(at+"."+k, t[k], skip); bad {
				return deep, true
			}
		}
	case []any:
		for i, e := range t {
			if deep, bad := firstNullInConfig(fmt.Sprintf("%s[%d]", at, i), e, skip); bad {
				return deep, true
			}
		}
	}
	return "", false
}

// firstNonTOMLValue walks a structure map for values TOML 1.0 cannot
// encode. Null is the one JSON/YAML value with no TOML spelling; every
// other scalar, array (mixed-type arrays are legal TOML 1.0), and map
// has one, so the promised well-formed output would otherwise fail at
// render time.
func firstNonTOMLValue(at string, v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return at, true
	case map[string]any:
		// Sorted, so the diagnostic names the same offending field on
		// every run; map iteration order would randomize it.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if deep, bad := firstNonTOMLValue(at+"."+k, t[k]); bad {
				return deep, true
			}
		}
	case []any:
		for i, e := range t {
			if deep, bad := firstNonTOMLValue(fmt.Sprintf("%s[%d]", at, i), e); bad {
				return deep, true
			}
		}
	}
	return "", false
}

// validateInjectWithinAllow is the cross-entry invariant: every inject
// domain must appear in the matching phase of the network policy's
// allow list. It reads the policy in the version-agnostic shape, so the
// invariant holds for a kit on either version.
func validateInjectWithinAllow(needs []Capability) error {
	policy, err := NetworkPolicyV2Of(needs)
	if err != nil {
		return err
	}
	for i, n := range needs {
		if n.Type != CapabilityCredential {
			continue
		}
		var c Credential
		if err := DecodeCapabilityConfig(n, &c); err != nil {
			return err
		}
		if c.APIKey == nil {
			continue
		}
		allow := phaseAllow(policy, c.Phase)
		for j, inj := range c.APIKey.Inject {
			// A bare "*" (or "**") allow entry grants every host, so any
			// inject domain is covered. Narrower glob patterns are not
			// expanded here: the exact-match rule keeps published inject
			// domains auditable against the allow list without
			// reimplementing the enforcement matcher.
			if !allow[stripPort(inj.Domain)] && !allow["*"] && !allow["**"] {
				return fieldErrorf(fmt.Sprintf("capabilities[%d].config.apiKey.inject[%d].domain", i, j),
					"capabilities[%d] (%s): inject domain %q is not in the network policy's %s allow list", i, c.Service, inj.Domain, c.Phase)
			}
		}
	}
	return nil
}

func phaseAllow(n *PhasedNetworkV2, phase string) map[string]bool {
	allow := map[string]bool{}
	if n == nil {
		return allow
	}
	var rules *NetworkRulesV2
	if phase == "install" {
		rules = n.Install
	} else {
		rules = n.Runtime
	}
	if rules == nil {
		return allow
	}
	// Every allowed host counts, bounded or not: an inject domain the
	// phase reaches only for GET is still a domain it reaches, and
	// whether the credential's own requests match the bound is the
	// runtime's question at enforcement, not one this check can answer.
	for _, e := range rules.Allow {
		for _, h := range e.Hosts {
			allow[stripPort(h)] = true
		}
	}
	return allow
}

// stripPort normalizes "host:443" to "host" for allow-list membership.
func stripPort(domain string) string {
	host, _, found := strings.Cut(domain, ":")
	if found {
		return host
	}
	return domain
}

func validateArgs(args map[string]Arg) error {
	for name, a := range args {
		path := "args." + name
		if !envVarName.MatchString(name) {
			return fieldErrorf(path, "args.%s: invalid arg name", name)
		}
		if a.Required && a.Default != nil {
			return fieldErrorf(path, "args.%s: required and default are mutually exclusive", name)
		}
		if len(a.Enum) > 0 && a.Pattern != "" {
			return fieldErrorf(path, "args.%s: enum and pattern are mutually exclusive", name)
		}
		if a.Pattern != "" {
			if _, err := regexp.Compile(a.Pattern); err != nil {
				return fieldErrorf(path+".pattern", "args.%s: invalid pattern: %v", name, err)
			}
		}
		if a.Env != "" && a.BuildArg != "" {
			return fieldErrorf(path, "args.%s: env and buildArg are mutually exclusive; an arg resolves in one phase", name)
		}
		if a.Env != "" && !envVarName.MatchString(a.Env) {
			return fieldErrorf(path+".env", "args.%s: env %q is not a valid env var name", name, a.Env)
		}
		if a.BuildArg != "" && !envVarName.MatchString(a.BuildArg) {
			return fieldErrorf(path+".buildArg", "args.%s: buildArg %q is not a valid build-arg name", name, a.BuildArg)
		}
	}
	return nil
}

// validateLifecycle checks one lifecycle capability's hooks and files.
// base is the entry's dotted path ("capabilities[N]"), so errors point
// into the config the author wrote.
func validateLifecycle(base string, i int, l *Lifecycle) error {
	for j, h := range l.Install {
		p := fmt.Sprintf("%s.config.install[%d]", base, j)
		if len(h.Command) == 0 {
			return fieldErrorf(p, "capabilities[%d]: install[%d]: command is required", i, j)
		}
		for k, e := range h.Env {
			if !envVarName.MatchString(e) {
				return fieldErrorf(fmt.Sprintf("%s.env[%d]", p, k), "capabilities[%d]: install[%d]: env entry %q is not a valid env var name", i, j, e)
			}
		}
	}
	for j, h := range l.Startup {
		p := fmt.Sprintf("%s.config.startup[%d]", base, j)
		if len(h.Command) == 0 {
			return fieldErrorf(p, "capabilities[%d]: startup[%d]: command is required", i, j)
		}
		for k, e := range h.Env {
			if !envVarName.MatchString(e) {
				return fieldErrorf(fmt.Sprintf("%s.env[%d]", p, k), "capabilities[%d]: startup[%d]: env entry %q is not a valid env var name", i, j, e)
			}
		}
	}
	for j, f := range l.Files {
		p := fmt.Sprintf("%s.config.files[%d]", base, j)
		if !strings.HasPrefix(f.Path, "/") {
			return fieldErrorf(p+".path", "capabilities[%d]: files[%d]: path %q must be absolute", i, j, f.Path)
		}
		if f.Mode != "" && !octalMode.MatchString(f.Mode) {
			return fieldErrorf(p+".mode", "capabilities[%d]: files[%d]: invalid octal mode %q", i, j, f.Mode)
		}
	}
	return nil
}
