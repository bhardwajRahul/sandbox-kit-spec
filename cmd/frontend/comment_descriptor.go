package main

import (
	"strings"
)

// Option G: a kit authored as one valid Dockerfile whose descriptor rides
// a `# kit:` comment block. The block is the contiguous run of comment
// lines starting at the marker; each line contributes its text after the
// comment marker, dedented by the two spaces that nested it under the
// marker. Comments are the one part of Dockerfile grammar that is
// lexically trivial and stable — line-start #, no continuations, no
// expansion — so this extraction owes nothing to the Dockerfile parser,
// and the untouched file remains the content recipe.

const kitCommentMarker = "# kit:"

// extractCommentDescriptor returns the descriptor YAML embedded in src's
// `# kit:` comment block, byte-faithful (dedent only, no re-marshaling),
// or ok=false when no marker line exists. Collection stops at the first
// non-comment line or at a comment that is not indented under the marker
// (trailing prose), so the block needs no explicit terminator.
func extractCommentDescriptor(src []byte) ([]byte, bool) {
	lines := strings.Split(string(src), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t\r") == kitCommentMarker {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, false
	}

	var out strings.Builder
	for _, line := range lines[start+1:] {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "#") {
			break
		}
		text := strings.TrimPrefix(line, "#")
		text = strings.TrimPrefix(text, " ")
		if text == "" {
			out.WriteString("\n")
			continue
		}
		if !strings.HasPrefix(text, "  ") {
			break
		}
		out.WriteString(text[2:] + "\n")
	}
	return []byte(out.String()), true
}

// contentRecipe derives the content handed to the recipe frontend from a
// comment-descriptor file. A second `# syntax=` stanza in the leading
// comment/blank region declares the content's OWN frontend — a pinned
// dockerfile release, labs syntax, or a foreign frontend entirely — and
// the content starts there: slicing makes the stanza the first line of
// the synthesized input, the only position where directives are honored,
// and dockerfile.v0's own forwarding does the rest. Without a second
// stanza the content is the whole file with the leading directive
// neutralized — that directive necessarily names this frontend (it is
// how the build got here), and forwarding it would recurse.
//
// The scan stops at the first instruction: a stanza below one would not
// be a directive in any reading of the file, and slicing there would
// change what builds.
func contentRecipe(src []byte) []byte {
	s := string(src)
	offset := 0
	for line := 0; offset < len(s); line++ {
		lineEnd := strings.IndexByte(s[offset:], '\n')
		if lineEnd == -1 {
			lineEnd = len(s) - offset
		}
		text := strings.TrimSpace(strings.TrimRight(s[offset:offset+lineEnd], "\r"))
		if line > 0 && strings.HasPrefix(text, "# syntax=") {
			return []byte(s[offset:])
		}
		if line > 0 && text != "" && !strings.HasPrefix(text, "#") {
			break
		}
		offset += lineEnd + 1
	}
	return neutralizeSyntaxLine(src)
}

// neutralizeSyntaxLine blanks a leading `# syntax=` directive so the file
// can be handed to dockerfile.v0 as content. Only the first line can be a
// directive, so only the first line is touched.
func neutralizeSyntaxLine(src []byte) []byte {
	first, rest, found := strings.Cut(string(src), "\n")
	if !strings.HasPrefix(strings.TrimSpace(first), "# syntax=") {
		return src
	}
	neutral := "# (syntax directive consumed by the kit frontend)"
	if !found {
		return []byte(neutral)
	}
	return []byte(neutral + "\n" + rest)
}
