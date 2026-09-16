package report

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Options shape one rendering of a report. The zero value is plain text
// at a conservative width, which is what a pipe, a log, and a test
// failure message all want.
type Options struct {
	// Verbose lists the checks that passed. They are silent by default:
	// a conforming run's news is that there is none.
	Verbose bool
	// Color paints severities. Callers decide from the terminal and the
	// environment; the renderer does not look.
	Color bool
	// Hyperlinks makes a requirement clickable where the terminal
	// understands OSC 8, so a link costs no line of its own.
	Hyperlinks bool
	// Width is where detail wraps.
	Width int
	// Link resolves a requirement to the specification text stating it,
	// returning empty for one that cannot be placed.
	Link func(requirement string) string
}

const (
	defaultWidth = 100
	// Columns: findings sit under a group label, and their detail under
	// a check-name column wide enough to keep the text edge straight
	// without pushing it off a narrow terminal.
	rowIndent    = 4
	minNameWidth = 20
	minTextWidth = 28
)

// Render writes the report: findings grouped by severity, worst first,
// then the tally. It writes nothing about the verdict — one report is a
// platform, and only the caller knows how many of them there were.
func (r Report) Render(w io.Writer, o Options) error {
	groups := []struct {
		label    string
		severity Severity
		rows     []row
	}{
		{label: "failed", severity: Fail},
		{label: "warned", severity: Warn},
		{label: "skipped", severity: Skip},
	}
	for i := range groups {
		for _, f := range r.Findings {
			if f.Severity == groups[i].severity {
				groups[i].rows = append(groups[i].rows, row{
					severity: f.Severity, name: f.Check, detail: f.Detail, requirement: f.Requirement,
				})
			}
		}
	}
	var passed []row
	if o.Verbose {
		for _, c := range r.passing() {
			passed = append(passed, row{severity: pass, name: c.Name, requirement: c.Requirement})
		}
	}

	style := palette{color: o.Color, hyperlinks: o.Hyperlinks, link: o.Link}
	width := o.Width
	if width <= 0 {
		width = defaultWidth
	}
	// Two layouts, chosen by what the run is named. Kit checks have
	// short names and read best in a column with the detail beside
	// them; runtime checks are named by the requirement they judge and
	// run long, and holding a column that wide would leave the detail
	// a strip too narrow to read, so their detail goes underneath.
	longest := widest(passed)
	for _, g := range groups {
		longest = max(longest, widest(g.rows))
	}
	shape := layout{
		width:     width,
		nameWidth: max(longest, minNameWidth),
		stacked:   longest > width*2/5,
	}

	out := &errWriter{w: w}
	for _, g := range groups {
		if len(g.rows) == 0 {
			continue
		}
		out.line("  " + style.dim(g.label))
		for _, f := range g.rows {
			f.render(out, style, shape)
		}
	}
	if len(passed) > 0 {
		out.line("  " + style.dim("passed"))
		for _, p := range passed {
			p.render(out, style, shape)
		}
	}
	out.line("  " + style.dim(r.Summary().String()))
	return out.err
}

func (r Report) String() string {
	var b strings.Builder
	_ = r.Render(&b, Options{})
	return strings.TrimRight(b.String(), "\n")
}

// String is the tally as a line: every check accounted for, in the order
// a reader cares about them.
func (s Summary) String() string {
	parts := []string{plural(s.Checks, "check")}
	for _, part := range []struct {
		count int
		label string
	}{
		{s.Failed, "failed"},
		{s.Warned, "warned"},
		{s.Skipped, "skipped"},
		{s.Passed, "passed"},
	} {
		if part.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", part.count, part.label))
		}
	}
	return strings.Join(parts, " · ")
}

// passing lists the checks that reported nothing, which is what passing
// looks like in a suite whose findings are all complaints.
func (r Report) passing() []Check {
	judged := make(map[string]bool, len(r.Findings))
	for _, f := range r.Findings {
		judged[f.Check] = true
	}
	var out []Check
	for _, c := range r.Checks {
		if !judged[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// pass is a severity only the renderer knows: the suites never report a
// passing check, they simply say nothing about it.
const pass = Severity(-1)

type row struct {
	severity    Severity
	name        string
	detail      string
	requirement string
}

// layout is how one report's rows are arranged: the width to wrap at,
// the name column, and whether detail sits beside a name or under it.
type layout struct {
	width     int
	nameWidth int
	stacked   bool
}

// render writes one finding: a symbol, the check, and its detail wrapped
// under a straight left edge, with the requirement trailing as the link
// to what was judged.
func (f row) render(out *errWriter, style palette, shape layout) {
	// A runtime check is named by the requirement it judges, so the two
	// are one string and printing it twice would say nothing twice. The
	// name carries the link instead.
	tag := f.requirement
	if tag == f.name {
		tag = ""
	}
	name := style.name(f.severity, f.name)
	if tag == "" {
		name = style.linked(f.requirement, name)
	}

	// Where detail starts: the indent, the symbol and its space, the
	// name column and the space after it. Continuations line up there.
	// A stacked row puts the name on a line of its own and its detail
	// back under the symbol, which is where a name too wide for the
	// column goes too.
	long := shape.stacked || columns(f.name) > shape.nameWidth
	column := rowIndent + 2 + shape.nameWidth + 1
	if long {
		column = rowIndent + 2
	}
	text := max(shape.width-column, minTextWidth)
	indent := strings.Repeat(" ", column)
	lines := wrap(f.detail, text)
	if tag != "" {
		tag = "[" + tag + "]"
		// On the last line when it fits, which is where a reader's eye
		// already is; on its own line when it does not, rather than
		// pushed past the edge where a narrow terminal would fold it.
		if last := len(lines) - 1; last >= 0 && columns(lines[last])+1+columns(tag) <= text {
			lines[last] += " " + style.dim(style.linked(f.requirement, tag))
		} else {
			lines = append(lines, style.dim(style.linked(f.requirement, tag)))
		}
	}

	head := strings.Repeat(" ", rowIndent) + style.symbol(f.severity) + " "
	if long {
		out.line(head + name)
		for _, line := range lines {
			out.line(indent + line)
		}
		return
	}
	head += pad(name, f.name, shape.nameWidth)
	if len(lines) == 0 {
		out.line(strings.TrimRight(head, " "))
		return
	}
	out.line(head + " " + lines[0])
	for _, line := range lines[1:] {
		out.line(indent + line)
	}
}

// wrap breaks text on spaces at width. A word longer than the column —
// a reference, a path — is left whole: breaking it would make it
// uncopyable to spare an edge.
func wrap(text string, width int) []string {
	if text == "" {
		return nil
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case columns(line)+1+columns(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	return append(lines, line)
}

// columns is how wide a string prints. The pages are written with em
// dashes and typographic quotes, and a finding quotes them back, so
// counting bytes would wrap early and misalign every line under it.
func columns(s string) int { return utf8.RuneCountInString(s) }

// palette paints, or does not. Everything the renderer colors goes
// through it, so a plain run is the same text without the escapes.
type palette struct {
	color      bool
	hyperlinks bool
	link       func(requirement string) string
}

const (
	red    = "31"
	yellow = "33"
	green  = "32"
	dim    = "2"
)

func (p palette) paint(code, s string) string {
	if !p.color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p palette) dim(s string) string { return p.paint(dim, s) }

func (p palette) symbol(s Severity) string {
	switch s {
	case Fail:
		return p.paint(red, "✗")
	case Warn:
		return p.paint(yellow, "!")
	case Skip:
		return p.paint(dim, "–")
	default:
		return p.paint(green, "✓")
	}
}

func (p palette) name(s Severity, name string) string {
	switch s {
	case Fail:
		return p.paint(red, name)
	case Warn:
		return p.paint(yellow, name)
	default:
		return p.paint(dim, name)
	}
}

// linked points text at the specification statement a requirement names,
// where the terminal understands OSC 8. Where it does not, the URL is
// left to the caller's reference list: a line per finding would bury the
// findings it is there to explain.
func (p palette) linked(requirement, text string) string {
	if p.link == nil || !p.hyperlinks || requirement == "" {
		return text
	}
	url := p.link(requirement)
	if url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// pad widens a styled string to a plain string's column width: escapes
// have no width, so the padding is measured on the text they wrap.
func pad(styled, plain string, width int) string {
	if columns(plain) >= width {
		return styled
	}
	return styled + strings.Repeat(" ", width-columns(plain))
}

func widest(rows []row) int {
	out := 0
	for _, r := range rows {
		out = max(out, columns(r.name))
	}
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// errWriter keeps the first write error and stops, so the renderer reads
// as a sequence of lines rather than as error handling.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) line(s string) {
	if e.err != nil {
		return
	}
	_, e.err = io.WriteString(e.w, s+"\n")
}
