package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Typed accessors over the needs list. Consumers (the frontend, the
// resolver, the runtime) read capabilities through these instead of
// walking entries: well-known configs decode strictly against the schema
// their type's version pins, so a typo in a known config is an error
// here, not a silently ignored key.

// DecodeCapabilityConfig decodes one entry's config into a well-known type's
// config struct, rejecting unknown fields.
func DecodeCapabilityConfig(n Capability, out any) error {
	data, err := json.Marshal(n.Config)
	if err != nil {
		return fmt.Errorf("capability %s: encode config: %w", n.Type, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("capability %s: %w", n.Type, err)
	}
	return nil
}

// toConfigMap is DecodeCapabilityConfig's inverse: it renders a
// well-known config struct back into the untyped map the grammar carries.
// The same JSON round trip both ways is what makes a synthesized entry —
// the merge's output — decode into exactly the struct it came from, and
// omitempty on those structs is what keeps an untouched field out of the
// published descriptor.
func toConfigMap(config any) (map[string]any, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CapabilityWithConfig returns the entry with its config replaced by the
// encoding of config, keeping everything that describes the request
// rather than stating it (type, optional, description). Callers that
// rewrite one well-known config — the frontend inlining a context body —
// go through this so the rewritten entry decodes exactly as an authored
// one would.
func CapabilityWithConfig(n Capability, config any) (*Capability, error) {
	raw, err := toConfigMap(config)
	if err != nil {
		return nil, fmt.Errorf("capability %s: encode config: %w", n.Type, err)
	}
	out := n
	out.Config = raw
	return &out, nil
}

// CapabilityTypes renders AnnotationCapabilities's value: the requested
// types, deduplicated, sorted, comma-joined. Sorted rather than authored
// order because the value is an existence index where order carries no
// meaning — sorting makes it canonical, so kits requesting the same types
// carry byte-identical values. Empty for an empty list, which callers
// translate to omitting the annotation.
func CapabilityTypes(capabilities []Capability) string {
	seen := make(map[string]bool, len(capabilities))
	types := make([]string, 0, len(capabilities))
	for _, c := range capabilities {
		if seen[c.Type] {
			continue
		}
		seen[c.Type] = true
		types = append(types, c.Type)
	}
	sort.Strings(types)
	return strings.Join(types, ",")
}

// HasCapability reports whether a type is requested.
func HasCapability(needs []Capability, typ string) bool {
	for _, n := range needs {
		if n.Type == typ {
			return true
		}
	}
	return false
}

// PrivilegedOf reports whether the descriptor requests privileged
// execution.
func PrivilegedOf(needs []Capability) bool {
	return HasCapability(needs, CapabilityPrivileged)
}

// NetworkPolicyOf returns the network-policy@1 request's config, or nil
// when none is declared. A descriptor on @2 has no @1 entry and reads as
// nil here; callers that must see either version use NetworkPolicyV2Of.
func NetworkPolicyOf(needs []Capability) (*PhasedNetwork, error) {
	for _, n := range needs {
		if n.Type != CapabilityNetworkPolicy {
			continue
		}
		var p PhasedNetwork
		if err := DecodeCapabilityConfig(n, &p); err != nil {
			return nil, err
		}
		return &p, nil
	}
	return nil, nil
}

// NetworkPolicyV2Of returns the network-policy request's config in the @2
// shape whichever version declared it, or nil when neither is. An @1
// config lifts into @2 as unbounded entries, which is what it means:
// every method and path on the hosts it allows. Consumers that read the
// policy to enforce it use this, so a kit does not change what they see
// by moving between versions.
func NetworkPolicyV2Of(needs []Capability) (*PhasedNetworkV2, error) {
	for _, n := range needs {
		switch n.Type {
		case CapabilityNetworkPolicyV2:
			var p PhasedNetworkV2
			if err := DecodeCapabilityConfig(n, &p); err != nil {
				return nil, err
			}
			return &p, nil
		case CapabilityNetworkPolicy:
			var p PhasedNetwork
			if err := DecodeCapabilityConfig(n, &p); err != nil {
				return nil, err
			}
			return &PhasedNetworkV2{
				Install: liftNetworkRules(p.Install),
				Runtime: liftNetworkRules(p.Runtime),
			}, nil
		}
	}
	return nil, nil
}

func liftNetworkRules(r *NetworkRules) *NetworkRulesV2 {
	if r == nil {
		return nil
	}
	return &NetworkRulesV2{Allow: hostEntries(r.Allow), Deny: hostEntries(r.Deny)}
}

// hostEntries renders @1's bare host lists as unbounded @2 entries.
func hostEntries(hosts []string) []NetworkEntry {
	if len(hosts) == 0 {
		return nil
	}
	out := make([]NetworkEntry, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, NetworkEntry{Hosts: []string{h}})
	}
	return out
}

// EntryMethods renders an entry's methods as enforcement reads them:
// MethodAny when it named none, the stated tokens otherwise.
func EntryMethods(e NetworkEntry) []string {
	if len(e.Methods) == 0 {
		return []string{MethodAny}
	}
	return e.Methods
}

// EntryPaths renders an entry's paths as enforcement reads them: every
// path when it named none, the stated globs otherwise.
func EntryPaths(e NetworkEntry) []string {
	if len(e.Paths) == 0 {
		return []string{"/**"}
	}
	return e.Paths
}

// CredentialCapability is one decoded credential request with its entry-level
// declaration state.
type CredentialCapability struct {
	Credential

	// Required mirrors the entry's optionality: a required credential
	// fails preflight when no binding satisfies the service, an optional
	// one is skipped and recorded.
	Required bool

	// Description is the entry's human-readable label.
	Description string
}

// CredentialsOf returns every credential request, in declaration order.
func CredentialsOf(needs []Capability) ([]CredentialCapability, error) {
	var out []CredentialCapability
	for _, n := range needs {
		if n.Type != CapabilityCredential {
			continue
		}
		var c Credential
		if err := DecodeCapabilityConfig(n, &c); err != nil {
			return nil, err
		}
		out = append(out, CredentialCapability{Credential: c, Required: !n.Optional, Description: n.Description})
	}
	return out, nil
}

// CredentialsOfPhase returns the credential requests for one phase.
func CredentialsOfPhase(needs []Capability, phase string) ([]CredentialCapability, error) {
	all, err := CredentialsOf(needs)
	if err != nil {
		return nil, err
	}
	var out []CredentialCapability
	for _, c := range all {
		if c.Phase == phase {
			out = append(out, c)
		}
	}
	return out, nil
}

// AgentSkillsOf returns every skills request, in declaration order,
// carrying each entry's optional flag: the store may be absent or the
// host may have skills off, and a runtime has to refuse a required entry
// it cannot satisfy while skipping an optional one.
func AgentSkillsOf(needs []Capability) ([]AgentSkillsCapability, error) {
	var out []AgentSkillsCapability
	for _, n := range needs {
		if n.Type != CapabilityAgentSkills {
			continue
		}
		var s AgentSkills
		if err := DecodeCapabilityConfig(n, &s); err != nil {
			return nil, err
		}
		out = append(out, AgentSkillsCapability{
			AgentSkills: s,
			Optional:    n.Optional,
			Description: n.Description,
		})
	}
	return out, nil
}

// VolumesOf returns every volume request, in declaration order.
func VolumesOf(needs []Capability) ([]Volume, error) {
	var out []Volume
	for _, n := range needs {
		if n.Type != CapabilityVolume {
			continue
		}
		var v Volume
		if err := DecodeCapabilityConfig(n, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// PortsOf returns every port request, in declaration order.
func PortsOf(needs []Capability) ([]Port, error) {
	var out []Port
	for _, n := range needs {
		if n.Type != CapabilityPort {
			continue
		}
		var p Port
		if err := DecodeCapabilityConfig(n, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// USBDeviceCapability is one decoded usb-device request with its entry-level
// declaration state.
type USBDeviceCapability struct {
	USBDevice

	Optional    bool
	Description string
}

// USBDevicesOf returns every usb-device request, in declaration order.
func USBDevicesOf(needs []Capability) ([]USBDeviceCapability, error) {
	var out []USBDeviceCapability
	for _, n := range needs {
		if n.Type != CapabilityUSBDevice {
			continue
		}
		var u USBDevice
		if err := DecodeCapabilityConfig(n, &u); err != nil {
			return nil, err
		}
		out = append(out, USBDeviceCapability{USBDevice: u, Optional: n.Optional, Description: n.Description})
	}
	return out, nil
}

// ResourcesOf returns the resources request's config, or nil when none
// is declared.
func ResourcesOf(needs []Capability) (*Resources, error) {
	for _, n := range needs {
		if n.Type != CapabilityResources {
			continue
		}
		var r Resources
		if err := DecodeCapabilityConfig(n, &r); err != nil {
			return nil, err
		}
		return &r, nil
	}
	return nil, nil
}

// LifecycleOf returns the lifecycle declaration's config, or nil when
// none is declared.
func LifecycleOf(capabilities []Capability) (*Lifecycle, error) {
	for _, c := range capabilities {
		if c.Type != CapabilityLifecycle {
			continue
		}
		var l Lifecycle
		if err := DecodeCapabilityConfig(c, &l); err != nil {
			return nil, err
		}
		return &l, nil
	}
	return nil, nil
}

// AgentContextOf returns the agent-context declaration's config, or nil
// when none is declared.
func AgentContextOf(capabilities []Capability) (*AgentContext, error) {
	for _, c := range capabilities {
		if c.Type != CapabilityAgentContext {
			continue
		}
		var a AgentContext
		if err := DecodeCapabilityConfig(c, &a); err != nil {
			return nil, err
		}
		return &a, nil
	}
	return nil, nil
}

// AgentSessionsOf returns the agent-sessions declaration's config, or
// nil when none is declared.
func AgentSessionsOf(needs []Capability) (*AgentSessions, error) {
	for _, n := range needs {
		if n.Type != CapabilityAgentSessions {
			continue
		}
		var a AgentSessions
		if err := DecodeCapabilityConfig(n, &a); err != nil {
			return nil, err
		}
		return &a, nil
	}
	return nil, nil
}
