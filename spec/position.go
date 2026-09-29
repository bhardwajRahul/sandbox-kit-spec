package spec

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Position is the 1-based source location of one YAML element.
type Position struct {
	Line      int
	Column    int
	EndLine   int
	EndColumn int
}

// Positions maps every element of a descriptor document to its source
// location, keyed by dotted path with zero-based sequence indices —
// "capabilities[0].config.service", "args.version.pattern",
// "provides[2]". Consumers (the frontend) join these with FieldError.Path
// to point a validation error at the exact lines that caused it.
func Positions(raw []byte) map[string]Position {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil
	}
	doc := &root
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		doc = root.Content[0]
	}
	if doc.Line == 0 {
		return nil
	}
	positions := map[string]Position{"": positionOf(doc)}
	collectPositions(doc, "", positions)
	return positions
}

func collectPositions(n *yaml.Node, path string, positions map[string]Position) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			childPath := key.Value
			if path != "" {
				childPath = path + "." + key.Value
			}
			positions[childPath] = positionOf(value)
			collectPositions(value, childPath, positions)
		}
	case yaml.SequenceNode:
		for i, item := range n.Content {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			positions[childPath] = positionOf(item)
			collectPositions(item, childPath, positions)
		}
	}
}

func positionOf(n *yaml.Node) Position {
	endLine, endColumn := endPositionOf(n)
	return Position{Line: n.Line, Column: n.Column, EndLine: endLine, EndColumn: endColumn}
}

func endPositionOf(n *yaml.Node) (int, int) {
	switch n.Kind {
	case yaml.ScalarNode:
		lines := strings.Split(n.Value, "\n")
		if len(lines) == 1 {
			return n.Line, n.Column + max(0, len(n.Value)-1)
		}
		return n.Line + len(lines) - 1, len(lines[len(lines)-1])
	case yaml.MappingNode, yaml.SequenceNode:
		if len(n.Content) == 0 {
			return n.Line, n.Column
		}
		return endPositionOf(n.Content[len(n.Content)-1])
	default:
		return n.Line, n.Column
	}
}

// FieldError is a validation failure attributed to one descriptor element.
// Path uses the same dotted form Positions produces, so an error and the
// source location it belongs to join without string parsing.
type FieldError struct {
	Path string
	err  error
}

func (e *FieldError) Error() string {
	if e.Path == "" {
		return e.err.Error()
	}
	if strings.HasPrefix(e.err.Error(), e.Path+": ") {
		return e.err.Error()
	}
	return e.Path + ": " + e.err.Error()
}

func (e *FieldError) Unwrap() error { return e.err }

func fieldErrorf(path, format string, args ...any) error {
	return &FieldError{Path: path, err: fmt.Errorf(format, args...)}
}

// argReferencePaths attributes raw-reference checks to the fields using
// an arg, rather than its declaration. Comments have no field path and
// remain document-level diagnostics, as do unparseable raw bytes.
func argReferencePaths(raw []byte) map[string][]string {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil
	}
	paths := map[string][]string{}
	var visit func(*yaml.Node, string)
	visit = func(n *yaml.Node, path string) {
		switch n.Kind {
		case yaml.DocumentNode:
			for _, child := range n.Content {
				visit(child, path)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				key, value := n.Content[i], n.Content[i+1]
				childPath := key.Value
				if path != "" {
					childPath = path + "." + childPath
				}
				visit(key, childPath)
				visit(value, childPath)
			}
		case yaml.SequenceNode:
			for i, child := range n.Content {
				visit(child, fmt.Sprintf("%s[%d]", path, i))
			}
		case yaml.ScalarNode:
			for _, name := range ReferencedArgs([]byte(n.Value)) {
				paths[name] = append(paths[name], path)
			}
		}
	}
	visit(&root, "")
	return paths
}
