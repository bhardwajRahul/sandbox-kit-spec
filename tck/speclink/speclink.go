package speclink

import (
	"io/fs"
	"path"
	"strings"
	"sync"
	"unicode"

	specdocs "github.com/docker/sandbox-kit-spec/v3/docs/spec"
)

// repository is where the pages are published. Links are built against a
// ref rather than a branch alone: a released binary points at the text it
// was built from, which is the text its findings quote.
const repository = "https://github.com/docker/sandbox-kit-spec"

// Reference is a requirement's home: the page stating it, the heading it
// sits under, and the URL that lands there.
type Reference struct {
	Requirement string
	Page        string
	Anchor      string
	URL         string
}

// Resolver answers where requirements are stated. The zero value is not
// usable; call New.
type Resolver struct {
	ref  string
	once sync.Once
	// statements are the ids the pages anchor, each mapped to the
	// heading it lives under — the finest target a Markdown page offers,
	// since the anchors themselves are HTML comments and nothing links
	// to a comment.
	statements map[string]Reference
	// sections are heading numbers, per page, for the ids that name a
	// section rather than a statement.
	sections map[string]Reference
	pages    map[string]bool
}

// New returns a resolver linking at the version's own text. A development
// build has no published tag to point at, so it links at main.
func New(version string) *Resolver {
	ref := "main"
	if version != "" && version != "dev" {
		ref = version
		if !strings.HasPrefix(ref, "v") {
			ref = "v" + ref
		}
	}
	return &Resolver{ref: ref}
}

// Resolve locates a requirement, reporting false for one the pages do not
// state — an id from a newer suite than the pages this binary carries,
// or the adapter contract's own ids, which bind the harness rather than
// the specification.
func (r *Resolver) Resolve(requirement string) (Reference, bool) {
	r.once.Do(r.index)
	requirement = strings.TrimSpace(requirement)
	if requirement == "" {
		return Reference{}, false
	}
	// A statement id is exact, so it is tried first: a requirement
	// naming one is asking for that line, not for the section around it.
	if ref, ok := r.statements[requirement]; ok {
		return ref, true
	}
	page, rest := splitRequirement(requirement)
	if page == "" || !r.pages[page] {
		return Reference{}, false
	}
	// A statement the pages do not anchor still has a section, and a
	// section id has nothing finer; both land on the heading.
	if section, _, _ := strings.Cut(rest, "/"); section != "" {
		if ref, ok := r.sections[page+" §"+section]; ok {
			ref.Requirement = requirement
			return ref, true
		}
	}
	// A capability page is one document about one type: its top is the
	// statement's neighbourhood even without a section number.
	return r.reference(requirement, page, ""), true
}

// URL is Resolve reduced to the link, empty when the requirement does not
// resolve, for callers that only want to print one.
func (r *Resolver) URL(requirement string) string {
	ref, ok := r.Resolve(requirement)
	if !ok {
		return ""
	}
	return ref.URL
}

// splitRequirement names the page a requirement id belongs to and returns
// what is left of the id.
//
// Three shapes reach here, and each says its page differently: a section
// id names its document ("SPEC-v3 §9.3", "conformance.md §2.4"), a
// statement id names the capability whose page states it
// ("lifecycle@1/install-once"), and a bare capability type names that
// page alone.
func splitRequirement(requirement string) (page, rest string) {
	if doc, section, ok := strings.Cut(requirement, " §"); ok {
		doc = strings.TrimSpace(doc)
		if !strings.HasSuffix(doc, ".md") {
			doc += ".md"
		}
		return doc, section
	}
	capability, statement, _ := strings.Cut(requirement, "/")
	if !strings.Contains(capability, "@") {
		return "", ""
	}
	return path.Join("capabilities", "com.docker.sandbox", capability+".md"), statement
}

func (r *Resolver) reference(requirement, page, anchor string) Reference {
	url := repository + "/blob/" + r.ref + "/docs/spec/" + page
	if anchor != "" {
		url += "#" + anchor
	}
	return Reference{Requirement: requirement, Page: "docs/spec/" + page, Anchor: anchor, URL: url}
}

// index reads the embedded pages once, recording where every anchored
// statement and every numbered heading lives.
func (r *Resolver) index() {
	r.statements = map[string]Reference{}
	r.sections = map[string]Reference{}
	r.pages = map[string]bool{}
	_ = fs.WalkDir(specdocs.Pages, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".md") {
			return nil
		}
		body, err := specdocs.Pages.ReadFile(name)
		if err != nil {
			return nil
		}
		r.pages[name] = true
		r.indexPage(name, string(body))
		return nil
	})
}

func (r *Resolver) indexPage(page, body string) {
	heading := ""
	for _, line := range strings.Split(body, "\n") {
		if title, ok := headingTitle(line); ok {
			heading = slug(title)
			if number, ok := sectionNumber(title); ok {
				r.sections[page+" §"+number] = r.reference(page+" §"+number, page, heading)
			}
			continue
		}
		if id, ok := statementID(line); ok {
			r.statements[id] = r.reference(id, page, heading)
		}
	}
}

// headingTitle returns the text of an ATX heading line.
func headingTitle(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, "#")
	if len(trimmed) == len(line) || !strings.HasPrefix(trimmed, " ") {
		return "", false
	}
	return strings.TrimSpace(trimmed), true
}

// statementID returns the id an anchor comment declares. The pages carry
// one per normative line, which is the same identity the coverage guard
// matches checks against.
func statementID(line string) (string, bool) {
	_, rest, ok := strings.Cut(line, "<!-- tck: ")
	if !ok {
		return "", false
	}
	id, _, ok := strings.Cut(rest, " -->")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(id), true
}

// sectionNumber reads the number a heading opens with, so "### 9.3
// Annotations" answers a requirement that names §9.3. The trailing dot a
// top-level heading carries ("## 10. The OCI layout") is not part of the
// number an id spells.
func sectionNumber(title string) (string, bool) {
	number, _, _ := strings.Cut(title, " ")
	number = strings.TrimSuffix(number, ".")
	if number == "" {
		return "", false
	}
	for _, c := range number {
		if !unicode.IsDigit(c) && c != '.' {
			return "", false
		}
	}
	return number, true
}

// slug is the fragment a Markdown host derives from a heading:
// lowercased, punctuation dropped, spaces hyphenated.
func slug(title string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_':
			b.WriteRune(c)
		case c == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}
