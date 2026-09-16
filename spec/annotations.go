package spec

import "strings"

// Standard OCI image annotation keys the frontend populates from the
// descriptor, so generic registry tooling displays a kit's metadata
// without knowing the kit grammar. All of them are display metadata with
// exactly the descriptor fields' authority: self-asserted, never trust
// inputs.
const (
	OCIAnnotationTitle       = "org.opencontainers.image.title"
	OCIAnnotationDescription = "org.opencontainers.image.description"
	OCIAnnotationAuthors     = "org.opencontainers.image.authors"
	OCIAnnotationSource      = "org.opencontainers.image.source"
	OCIAnnotationLicenses    = "org.opencontainers.image.licenses"
	OCIAnnotationVersion     = "org.opencontainers.image.version"
)

// OCIAnnotations maps a published descriptor onto the standard
// org.opencontainers.image.* annotations it can populate: title,
// description, authors, source, and licenses straight from their fields,
// and version from the version: fallback or — when every versioned
// provides entry agrees — that one version. Empty fields yield no key,
// so absence means "not declared", and keys the descriptor cannot answer
// deterministically (created, revision, base.*) are never emitted here:
// a wall-clock stamp would break build reproducibility, and VCS state is
// the builder's knowledge, not the descriptor's.
func OCIAnnotations(d *Descriptor) map[string]string {
	out := map[string]string{}
	set := func(key, value string) {
		if value != "" {
			out[key] = value
		}
	}
	set(OCIAnnotationTitle, d.DisplayName)
	set(OCIAnnotationDescription, d.Description)
	set(OCIAnnotationAuthors, d.Author)
	set(OCIAnnotationSource, d.SourceURL)
	set(OCIAnnotationLicenses, strings.Join(d.Licenses, ","))
	set(OCIAnnotationVersion, descriptorVersion(d))
	return out
}

// descriptorVersion is the one version the descriptor names: the
// version: field when set, otherwise the version every versioned
// provides entry agrees on. Disagreement or absence yields "" — an
// ambiguous version is worse than none.
func descriptorVersion(d *Descriptor) string {
	if d.Version != "" {
		return d.Version
	}
	version := ""
	for _, s := range d.Provides {
		p, err := ParseProvide(s)
		if err != nil || p.Version == "" {
			continue
		}
		if version != "" && version != p.Version {
			return ""
		}
		version = p.Version
	}
	return version
}
