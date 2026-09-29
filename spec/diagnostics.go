package spec

import (
	"errors"
	"fmt"
	"strings"
)

// ValidationErrors contains independent failures in validation order. A
// validator returns nil on success, never an empty ValidationErrors.
// Each field failure remains accessible through errors.As and Unwrap.
type ValidationErrors []error

func (e ValidationErrors) Error() string {
	messages := make([]string, len(e))
	for i, err := range e {
		messages[i] = err.Error()
	}
	return strings.Join(messages, "\n")
}

func (e ValidationErrors) Unwrap() []error { return []error(e) }

func (e *ValidationErrors) add(err error) {
	if err == nil {
		return
	}
	if nested, ok := err.(ValidationErrors); ok {
		*e = append(*e, nested...)
	} else {
		*e = append(*e, err)
	}
}

func (e ValidationErrors) err() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// PositionFor finds a field's location, falling back to its nearest
// containing element when a required field is absent. The empty path
// denotes the document, so even missing top-level fields have a location.
func PositionFor(positions map[string]Position, path string) (Position, bool) {
	for {
		if pos, ok := positions[path]; ok {
			return pos, true
		}
		if path == "" {
			return Position{}, false
		}
		if i := strings.LastIndexAny(path, ".["); i >= 0 {
			path = path[:i]
		} else {
			path = ""
		}
	}
}

// WithSource renders every validation failure with a filename and, when
// available, a source position, source line, and caret. It preserves the
// original error tree for errors.Is/As. Callers without source bytes can
// use the field paths in Error() directly. Missing fields point at their
// containing element.
func WithSource(err error, filename string, raw []byte) error {
	if err == nil {
		return nil
	}
	return &sourceError{err: err, message: formatSourceError(err, filename, Positions(raw), strings.Split(string(raw), "\n"))}
}

type sourceError struct {
	err     error
	message string
}

func (e *sourceError) Error() string { return e.message }
func (e *sourceError) Unwrap() error { return e.err }

func formatSourceError(err error, filename string, positions map[string]Position, lines []string) string {
	var all ValidationErrors
	if errors.As(err, &all) {
		messages := make([]string, len(all))
		for i, child := range all {
			messages[i] = formatSourceError(child, filename, positions, lines)
		}
		// Preserve contextual wrappers such as "validate kit: %w".
		return strings.Replace(err.Error(), all.Error(), strings.Join(messages, "\n\n"), 1)
	}
	prefix := filename
	var excerpt string
	var field *FieldError
	if errors.As(err, &field) {
		if pos, ok := PositionFor(positions, field.Path); ok {
			if prefix != "" {
				prefix += ":"
			}
			prefix += fmt.Sprintf("%d:%d", pos.Line, pos.Column)
			excerpt = sourceExcerpt(lines, pos)
		}
	}
	if prefix == "" {
		return err.Error()
	}
	return prefix + ": " + err.Error() + excerpt
}

// YAML positions identify a node's start precisely, but its decoded value
// cannot give the token's width (quotes, escapes, and folded blocks change
// it). Mark the start rather than underlining an inferred source range.
func sourceExcerpt(lines []string, pos Position) string {
	if pos.Line < 1 || pos.Line > len(lines) || pos.Column < 1 {
		return ""
	}
	line := strings.TrimSuffix(lines[pos.Line-1], "\r")
	runes := []rune(line)
	if pos.Column > len(runes)+1 {
		return ""
	}
	var padding strings.Builder
	for _, r := range runes[:pos.Column-1] {
		// Both gutters have the same width, so tabs reach the same terminal
		// stops in the source and marker lines.
		if r == '\t' {
			padding.WriteRune(r)
		} else {
			padding.WriteByte(' ')
		}
	}
	number := fmt.Sprint(pos.Line)
	gutter := strings.Repeat(" ", len(number))
	return "\n" + gutter + " |\n" + number + " | " + line + "\n" + gutter + " | " + padding.String() + "^"
}
