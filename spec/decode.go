package spec

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Decode parses descriptor YAML strictly: unknown fields are errors, because
// a misspelled key silently ignored is a policy silently absent. A leading
// `# syntax=` line needs no special handling — it is a YAML comment.
func Decode(data []byte) (*Descriptor, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var d Descriptor
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("decode kit descriptor: %w", err)
	}
	if d.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported schemaVersion %q (this loader reads %q)", d.SchemaVersion, SchemaVersion)
	}
	return &d, nil
}
