package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/docker/sandbox-kit-spec/v3/tck/report"
	"github.com/docker/sandbox-kit-spec/v3/tck/speclink"
)

// outcome is one invocation's result: the suite that ran, what it
// judged, and a report per platform.
type outcome struct {
	suite  string
	target string
	groups []group
}

// group is one report and the platforms that produced exactly it. A
// multi-platform artifact usually fails the same way everywhere —
// nothing about a missing annotation is per-architecture — and repeating
// an identical verdict eight times buries the one thing to fix.
type group struct {
	platforms []string
	report    report.Report
}

func (o *outcome) add(platform string, rep report.Report) {
	for i, g := range o.groups {
		if sameReport(g.report, rep) {
			o.groups[i].platforms = append(o.groups[i].platforms, platform)
			return
		}
	}
	var platforms []string
	if platform != "" {
		platforms = []string{platform}
	}
	o.groups = append(o.groups, group{platforms: platforms, report: rep})
}

func (o outcome) conforms() bool {
	for _, g := range o.groups {
		if g.report.Failed() {
			return false
		}
	}
	return true
}

// sameReport reports whether two platforms were judged identically. Only
// a report with a platform can be collapsed into another, so a
// single-manifest run keeps its own group.
func sameReport(a, b report.Report) bool {
	if len(a.Findings) != len(b.Findings) || len(a.Checks) != len(b.Checks) {
		return false
	}
	for i := range a.Findings {
		if a.Findings[i] != b.Findings[i] {
			return false
		}
	}
	for i := range a.Checks {
		if a.Checks[i] != b.Checks[i] {
			return false
		}
	}
	return true
}

// presentation is how a run is written down: the flags that shape it,
// and the resolver that turns a requirement into a place to read it.
type presentation struct {
	format  string
	color   string
	verbose bool
	spec    *speclink.Resolver
}

func addReportingFlags(fs *flag.FlagSet) *presentation {
	p := &presentation{spec: speclink.New(version)}
	fs.StringVar(&p.format, "format", "text", "text or json")
	fs.StringVar(&p.color, "color", "auto", "auto, always, or never")
	fs.BoolVar(&p.verbose, "verbose", false, "list the checks that passed")
	fs.BoolVar(&p.verbose, "v", false, "list the checks that passed")
	return p
}

// write reports the run and returns the verdict as an error, so a
// non-conforming subject fails the exit status without a second line
// saying what the report already said.
func (p *presentation) write(w io.Writer, o outcome) error {
	var err error
	switch p.format {
	case "json":
		err = p.writeJSON(w, o)
	case "text", "":
		err = p.writeText(w, o)
	default:
		return fmt.Errorf("unknown --format %q; text or json", p.format)
	}
	if err != nil {
		return err
	}
	if !o.conforms() {
		return errNotConformant
	}
	return nil
}

func (p *presentation) writeText(w io.Writer, o outcome) error {
	tty, width := terminal(w)
	color := p.painting(tty)
	options := report.Options{
		Verbose:    p.verbose,
		Color:      color,
		Hyperlinks: color && tty,
		Width:      width,
		Link:       p.spec.URL,
	}
	style := ansi(color)

	out := &lines{w: w}
	out.printf("%s", style(dim, fmt.Sprintf("kit-tck %s · %s · %s", version, o.suite, o.target)))
	for _, g := range o.groups {
		out.printf("")
		if len(g.platforms) > 0 {
			out.printf("%s", style(bold, strings.Join(g.platforms, ", ")))
		}
		if out.err == nil {
			out.err = g.report.Render(w, options)
		}
	}

	out.printf("")
	if o.conforms() {
		out.printf("%s", style(green, "✓ conforms"))
	} else {
		out.printf("%s", style(red, "✗ does not conform"))
	}
	// Where the terminal carries no hyperlink, the requirements on
	// screen are names without addresses; the list gives each one once,
	// which is cheaper than a URL under every finding that cites it.
	if !options.Hyperlinks {
		p.writeReferences(out, o, style)
	}
	return out.err
}

func (p *presentation) writeReferences(out *lines, o outcome, style func(string, string) string) {
	// Every requirement the run put on screen, once. A passing check
	// names one too, so the list follows --verbose rather than the
	// findings alone.
	var ids []string
	seen := map[string]bool{}
	cite := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, g := range o.groups {
		for _, f := range g.report.Findings {
			cite(f.Requirement)
		}
		if p.verbose {
			for _, c := range g.report.Checks {
				cite(c.Requirement)
			}
		}
	}
	var refs []speclink.Reference
	width := 0
	for _, id := range ids {
		ref, ok := p.spec.Resolve(id)
		if !ok {
			continue
		}
		refs = append(refs, ref)
		width = max(width, len(id))
	}
	if len(refs) == 0 {
		return
	}
	out.printf("")
	out.printf("%s", style(dim, "specification"))
	for _, ref := range refs {
		out.printf("  %-*s  %s", width, ref.Requirement, style(dim, ref.URL))
	}
}

// lines writes a line at a time and keeps the first error, so the
// presentation reads as what it prints rather than as error handling.
type lines struct {
	w   io.Writer
	err error
}

func (l *lines) printf(format string, args ...any) {
	if l.err != nil {
		return
	}
	_, l.err = fmt.Fprintf(l.w, format+"\n", args...)
}

// The JSON form is the run as data: every check accounted for, every
// finding carrying the statement it judged and where that statement is
// written, so a pipeline can annotate a diff without parsing prose.
type jsonRun struct {
	Tool     string       `json:"tool"`
	Version  string       `json:"version"`
	Suite    string       `json:"suite"`
	Target   string       `json:"target"`
	Conforms bool         `json:"conforms"`
	Results  []jsonResult `json:"results"`
}

type jsonResult struct {
	Platforms []string      `json:"platforms,omitempty"`
	Conforms  bool          `json:"conforms"`
	Summary   jsonSummary   `json:"summary"`
	Findings  []jsonFinding `json:"findings"`
	Passed    []string      `json:"passed"`
}

type jsonSummary struct {
	Checks  int `json:"checks"`
	Failed  int `json:"failed"`
	Warned  int `json:"warned"`
	Skipped int `json:"skipped"`
	Passed  int `json:"passed"`
}

type jsonFinding struct {
	Severity    string `json:"severity"`
	Check       string `json:"check"`
	Requirement string `json:"requirement"`
	Detail      string `json:"detail"`
	Spec        string `json:"spec,omitempty"`
}

func (p *presentation) writeJSON(w io.Writer, o outcome) error {
	out := jsonRun{
		Tool: "kit-tck", Version: version, Suite: o.suite, Target: o.target,
		Conforms: o.conforms(), Results: []jsonResult{},
	}
	for _, g := range o.groups {
		summary := g.report.Summary()
		result := jsonResult{
			Platforms: g.platforms,
			Conforms:  !g.report.Failed(),
			Summary: jsonSummary{
				Checks: summary.Checks, Failed: summary.Failed,
				Warned: summary.Warned, Skipped: summary.Skipped, Passed: summary.Passed,
			},
			Findings: []jsonFinding{},
			Passed:   []string{},
		}
		judged := map[string]bool{}
		for _, f := range g.report.Findings {
			judged[f.Check] = true
			result.Findings = append(result.Findings, jsonFinding{
				Severity:    strings.ToLower(f.Severity.String()),
				Check:       f.Check,
				Requirement: f.Requirement,
				Detail:      f.Detail,
				Spec:        p.spec.URL(f.Requirement),
			})
		}
		// Passing checks are named whatever --verbose says: the flag is
		// about what a reader wants to read, and a consumer that asked
		// for data wants the whole roster either way.
		for _, c := range g.report.Checks {
			if !judged[c.Name] {
				result.Passed = append(result.Passed, c.Name)
			}
		}
		out.Results = append(out.Results, result)
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}

// painting decides whether to emit escapes: what the flag says, then
// what the environment asks for, then what the terminal is.
func (p *presentation) painting(tty bool) bool {
	switch p.color {
	case "always":
		return true
	case "never":
		return false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return tty
}

// terminal reports whether the writer is one, and how wide. A pipe gets
// the renderer's own default so a redirected run reads the same
// everywhere, and so does a terminal that will not say how wide it is:
// zero is not a width, and taking it for one would wrap every line.
func terminal(w io.Writer) (bool, int) {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return false, 0
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil || width < 40 {
		return true, 0
	}
	return true, min(width, 140)
}

const (
	red   = "31"
	green = "32"
	bold  = "1"
	dim   = "2"
)

func ansi(color bool) func(code, s string) string {
	return func(code, s string) string {
		if !color {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}
}
