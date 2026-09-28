package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// CapabilityItem is the descriptor union: an ordinary Capability or Group.
// The alias keeps existing ordinary-entry Go literals source-compatible.
type CapabilityItem = Capability

// CapabilityGroup selects one feature's requests atomically.
type CapabilityGroup struct {
	Name         string       `json:"name,omitempty" yaml:"name,omitempty"`
	Description  string       `json:"description,omitempty" yaml:"description,omitempty"`
	Optional     bool         `json:"optional,omitempty" yaml:"optional,omitempty"`
	Capabilities []Capability `json:"capabilities" yaml:"capabilities"`
}

func (g *CapabilityGroup) UnmarshalYAML(unmarshal func(any) error) error {
	type plain CapabilityGroup
	if err := unmarshal((*plain)(g)); err != nil {
		return err
	}
	var keys map[string]yaml.Node
	if err := unmarshal(&keys); err != nil {
		return err
	}
	if n, ok := keys["name"]; ok && n.ShortTag() != "!!str" {
		return fmt.Errorf("group name must be a string")
	}
	return nil
}

func (g *CapabilityGroup) UnmarshalJSON(data []byte) error {
	type plain CapabilityGroup
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode((*plain)(g)); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	if n, ok := keys["name"]; ok && bytes.Equal(bytes.TrimSpace(n), []byte("null")) {
		return fmt.Errorf("group name must be a string")
	}
	return nil
}

// CapabilitySource survives publication for diagnostics, not trust decisions.
type CapabilitySource struct {
	Kit  string `json:"kit" yaml:"kit"`
	Path string `json:"path" yaml:"path"`
}

// SelectCapability answers whether the runtime will provide one expanded entry.
// It must not apply the entry: the enclosing group can still be rejected.
type SelectCapability func(Capability) bool

// Supported constructs a selector from a runtime's claimed type list.
func Supported(types ...string) SelectCapability {
	claimed := make(map[string]bool, len(types))
	for _, typ := range types {
		claimed[typ] = true
	}
	return func(c Capability) bool { return claimed[c.Type] }
}

// KnownCapabilities lists types understood by this version of the library.
// Understanding a schema is not a claim that a runtime implements it.
func KnownCapabilities() []string {
	return []string{CapabilityNetworkPolicy, CapabilityNetworkPolicyV2, CapabilityCredential,
		CapabilityVolume, CapabilityPort, CapabilityUSBDevice, CapabilityResources,
		CapabilityPrivileged, CapabilityLifecycle, CapabilityAgentContext, CapabilityAgentSessions,
		CapabilityAgentSkills, CapabilitySbx, CapabilityLongRunning, CapabilityKitRegistry}
}

// SelectionRecord identifies an original item and its evaluated members.
// Path is in the consumed descriptor; Source, when present, is publisher metadata.
type SelectionRecord struct {
	Path          string             `json:"path"`
	Name          string             `json:"name,omitempty"`
	Source        *CapabilitySource  `json:"source,omitempty"`
	Members       []string           `json:"members"`
	MemberSources []CapabilitySource `json:"memberSources,omitempty"`
	Rejected      []string           `json:"rejected,omitempty"`
}

// Selection retains the create-time decision separately from merged grants.
type Selection struct {
	Capabilities []Capability      `json:"capabilities"`
	Selected     []SelectionRecord `json:"selected,omitempty"`
	Skipped      []SelectionRecord `json:"skipped,omitempty"`
}

// HasGroups reports whether selection is needed before reconciliation.
func HasGroups(items []Capability) bool {
	for _, c := range items {
		if c.Group != nil || c.groupSet {
			return true
		}
	}
	return false
}

// MapCapabilities visits ordinary entries, including group members, without
// selecting them. It copies slices and groups; fn must copy a config before
// changing it. Publishers use it to stage every potentially selected body.
func MapCapabilities(items []Capability, fn func(Capability) (Capability, error)) ([]Capability, error) {
	out := make([]Capability, len(items))
	for i, c := range items {
		if c.Group == nil {
			var err error
			out[i], err = fn(c)
			if err != nil {
				return nil, err
			}
		} else {
			g := *c.Group
			var err error
			g.Capabilities, err = MapCapabilities(g.Capabilities, fn)
			if err != nil {
				return nil, err
			}
			c.Group = &g
			out[i] = c
		}
	}
	return out, nil
}

// DeclaredCapabilities enumerates ordinary requests, including unselected
// members. Use only for artifact inspection, never runtime application.
func DeclaredCapabilities(items []Capability) []Capability {
	var out []Capability
	for _, c := range items {
		if c.Group != nil {
			out = append(out, DeclaredCapabilities(c.Group.Capabilities)...)
		} else {
			out = append(out, c)
		}
	}
	return out
}

func validateCapabilityEntries(d *Descriptor) error {
	if !HasGroups(d.Capabilities) {
		return validateCapabilityBlock(d)
	}
	var errs ValidationErrors
	var ordinary []Capability
	var paths []string
	for i, c := range d.Capabilities {
		at := fmt.Sprintf("capabilities[%d]", i)
		if c.Source != nil && c.Source.Path == "" {
			errs.add(fieldErrorf(at+".source", "path is required"))
		}
		if c.Group == nil && !c.groupSet {
			ordinary = append(ordinary, c)
			paths = append(paths, at)
			continue
		}
		if c.Group == nil {
			errs.add(fieldErrorf(at+".group", "group must be an object"))
			continue
		}
		if c.Type != "" || c.Name != "" || c.Description != "" || c.Optional || c.optionalSet || c.ConfigStated() {
			errs.add(fieldErrorf(at, "item must be exactly one ordinary entry or group"))
		}
		if len(c.Group.Capabilities) == 0 {
			errs.add(fieldErrorf(at+".group.capabilities", "group must contain at least one member"))
		}
		cp := *d
		cp.declarationsOnly = true
		cp.Capabilities = c.Group.Capabilities
		memberPaths := make([]string, len(cp.Capabilities))
		for j, member := range cp.Capabilities {
			memberPaths[j] = fmt.Sprintf("%s.group.capabilities[%d]", at, j)
			if member.Group != nil || member.groupSet {
				errs.add(fieldErrorf(memberPaths[j], "nested groups are not permitted"))
			}
			if member.Optional || member.optionalSet {
				errs.add(fieldErrorf(memberPaths[j]+".optional", "group members must not state optional"))
			}
		}
		errs.add(remapCapabilityErrors(validateCapabilityBlock(&cp), memberPaths))
	}
	cp := *d
	cp.Capabilities = ordinary
	cp.declarationsOnly = true
	errs.add(remapCapabilityErrors(validateCapabilityBlock(&cp), paths))
	return errs.err()
}

func remapCapabilityErrors(err error, paths []string) error {
	if err == nil {
		return nil
	}
	if all, ok := err.(ValidationErrors); ok {
		out := make(ValidationErrors, len(all))
		for i, e := range all {
			out[i] = remapCapabilityErrors(e, paths)
		}
		return out
	}
	if field, ok := err.(*FieldError); ok {
		for i, to := range paths {
			from := fmt.Sprintf("capabilities[%d]", i)
			if field.Path == from || strings.HasPrefix(field.Path, from+".") {
				return &FieldError{Path: to + strings.TrimPrefix(field.Path, from), err: field.err}
			}
		}
	}
	return err
}

// ValidateDeclarations validates all structure and per-entry configurations,
// leaving selection-dependent cross-entry checks to ValidateEffective.
func ValidateDeclarations(raw []byte, d *Descriptor) ([]string, error) {
	cp := *d
	cp.declarationsOnly = true
	return ValidateRaw(raw, &cp)
}

// ValidateExpandedDeclarations additionally refuses unresolved arguments.
func ValidateExpandedDeclarations(raw []byte, d *Descriptor) ([]string, error) {
	cp := *d
	cp.declarationsOnly = true
	return ValidateEffective(raw, &cp)
}

// SelectCapabilities validates before calling the selector and flattens only
// wholly selected constructs. A nil selector is an error, never allow-all.
// Required rejection returns records alongside the error for diagnostics.
// Callback inputs, selected capabilities, and source records do not share
// mutable configuration or provenance with the original declarations.
func SelectCapabilities(items []CapabilityItem, selectCapability SelectCapability) (Selection, error) {
	var result Selection
	if selectCapability == nil {
		return result, fmt.Errorf("select capabilities: no selector")
	}
	d := &Descriptor{Kind: KindWorkload, Capabilities: items, declarationsOnly: true}
	if err := validateCapabilityEntries(d); err != nil {
		return result, err
	}
	for _, c := range DeclaredCapabilities(items) {
		if capabilityIsParameterized(c) {
			return result, fmt.Errorf("select capabilities: %s still contains unresolved arguments", c.Type)
		}
	}
	var errs ValidationErrors
	for i, item := range items {
		at := fmt.Sprintf("capabilities[%d]", i)
		record := SelectionRecord{Path: at, Name: item.Name, Source: item.Source}
		if record.Source != nil {
			source := *record.Source
			record.Source = &source
		}
		optional := item.Optional
		members := []Capability{item}
		if item.Group != nil {
			members = item.Group.Capabilities
			optional = item.Group.Optional
			record.Name = item.Group.Name
		}
		var selected []Capability
		for j, c := range members {
			c = cloneSelectionCapability(c)
			memberPath := at
			if item.Group != nil {
				memberPath = fmt.Sprintf("%s.group.capabilities[%d]", at, j)
			}
			record.Members = append(record.Members, memberPath)
			origin := CapabilitySource{Path: memberPath}
			if c.Source != nil {
				origin = *c.Source
			}
			record.MemberSources = append(record.MemberSources, origin)
			if !selectCapability(cloneSelectionCapability(c)) {
				record.Rejected = append(record.Rejected, memberPath)
			}
			if c.Source == nil {
				c.Source = &CapabilitySource{Path: memberPath}
			}
			selected = append(selected, c)
		}
		if len(record.Rejected) > 0 {
			result.Skipped = append(result.Skipped, record)
			if !optional {
				for _, memberPath := range record.Rejected {
					errs.add(fieldErrorf(memberPath, "required capability selection rejected (item %s)", at))
				}
			}
		} else {
			result.Selected = append(result.Selected, record)
			result.Capabilities = append(result.Capabilities, selected...)
		}
	}
	return result, errs.err()
}

func cloneSelectionCapability(c Capability) Capability {
	if c.Source != nil {
		source := *c.Source
		c.Source = &source
	}
	c.Config = cloneConfigValue(reflect.ValueOf(c.Config)).Interface().(map[string]any)
	return c
}

// Preserve concrete scalar and container types from Go callers as well as
// decoded YAML/JSON. A serialization round trip would change numeric types.
func cloneConfigValue(v reflect.Value) reflect.Value {
	out := reflect.New(v.Type()).Elem()
	out.Set(v)
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			if v.Kind() == reflect.Pointer {
				out.Set(reflect.New(v.Type().Elem()))
				out.Elem().Set(cloneConfigValue(v.Elem()))
			} else {
				out.Set(cloneConfigValue(v.Elem()))
			}
		}
	case reflect.Map:
		if !v.IsNil() {
			out.Set(reflect.MakeMapWithSize(v.Type(), v.Len()))
			iter := v.MapRange()
			for iter.Next() {
				out.SetMapIndex(iter.Key(), cloneConfigValue(iter.Value()))
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && !v.IsNil() {
			out.Set(reflect.MakeSlice(v.Type(), v.Len(), v.Len()))
		}
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneConfigValue(v.Index(i)))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if out.Field(i).CanSet() {
				out.Field(i).Set(cloneConfigValue(v.Field(i)))
			}
		}
	}
	return out
}

func contributionsHaveGroups(contributions []Contribution) bool {
	for _, c := range contributions {
		if c.Descriptor != nil && HasGroups(c.Descriptor.Capabilities) {
			return true
		}
	}
	return false
}

// preserveGroups publishes declaration order, not a hypothetical selection.
// Wrapping ordinary entries preserves their optionality and allows independent
// singleton contributions without inventing a second publishing-only grammar.
func preserveGroups(contributions []Contribution, opts MergeOptions) (*MergeResult, error) {
	stripped := make([]Contribution, len(contributions))
	for i, c := range contributions {
		if c.Descriptor == nil {
			return nil, fmt.Errorf("merge: %s has no descriptor", c.Reference)
		}
		d := *c.Descriptor
		d.Capabilities = nil
		stripped[i] = Contribution{Reference: c.Reference, Descriptor: &d}
	}
	result, err := mergeDeclarations(stripped)
	if err != nil {
		return nil, err
	}
	count := 0
	for _, contribution := range contributions {
		for i, item := range contribution.Descriptor.Capabilities {
			source := &CapabilitySource{Kit: contribution.Reference, Path: fmt.Sprintf("capabilities[%d]", i)}
			if item.Source != nil {
				source = item.Source
			}
			if item.Group == nil {
				member := item
				member.Optional = false
				member.optionalSet = false
				member.Source = source
				item = Capability{Group: &CapabilityGroup{Name: item.Name, Description: item.Description, Optional: item.Optional, Capabilities: []Capability{member}}, Source: source}
			} else {
				g := *item.Group
				g.Capabilities = append([]Capability(nil), g.Capabilities...)
				item.Group = &g
				item.Source = source
				for j := range g.Capabilities {
					if g.Capabilities[j].Source == nil {
						g.Capabilities[j].Source = &CapabilitySource{Kit: source.Kit, Path: fmt.Sprintf("%s.group.capabilities[%d]", source.Path, j)}
					}
				}
			}
			for j, member := range item.Group.Capabilities {
				if member.Type != CapabilityAgentContext {
					continue
				}
				var ac AgentContext
				if err := DecodeCapabilityConfig(member, &ac); err != nil {
					return nil, err
				}
				if ac.ContentFile == "" && ac.Content == "" {
					continue
				}
				if opts.ContextPath == "" {
					return nil, fmt.Errorf("merge: conditional agent-context requires ContextPath")
				}
				target := fmt.Sprintf("%s.parts/%d.md", opts.ContextPath, count)
				count++
				result.ContextSources = append(result.ContextSources, ContextSource{Reference: contribution.Reference, Path: ac.ContentFile, Content: ac.Content, Target: target})
				ac.Content = ""
				ac.ContentFile = target
				updated, err := CapabilityWithConfig(member, ac)
				if err != nil {
					return nil, err
				}
				item.Group.Capabilities[j] = *updated
			}
			result.Descriptor.Capabilities = append(result.Descriptor.Capabilities, item)
		}
	}
	return result, nil
}
