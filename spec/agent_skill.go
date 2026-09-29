package spec

import (
	"fmt"
	"path"
	"regexp"
)

// AgentSkill identifies a complete skill directory in the published image.
// Name overrides its basename at discovery destinations, not in the source tree.
type AgentSkill struct {
	Path string `yaml:"path" json:"path"`
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
}

// AgentSkillsDirectory is an agent's discovery destination for bundled skills.
// Host store access is a separate AgentSkills request.
type AgentSkillsDirectory struct {
	Path string `yaml:"path" json:"path"`
}

// AgentSkillCapability retains the display label separately from config.Name.
type AgentSkillCapability struct {
	AgentSkill
	Optional    bool
	Name        string
	Description string
}

// AgentSkillsDirectoryCapability retains the selection fields of a destination.
type AgentSkillsDirectoryCapability struct {
	AgentSkillsDirectory
	Optional    bool
	Name        string
	Description string
}

var agentSkillName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// AgentSkillName returns the directory component used at every destination.
func AgentSkillName(s AgentSkill) string {
	if s.Name != "" {
		return s.Name
	}
	return path.Base(s.Path)
}

func validateBundledSkill(field string, n Capability) (string, error) {
	var source, key string
	if n.Type == CapabilityAgentSkill {
		var s AgentSkill
		if err := DecodeCapabilityConfig(n, &s); err != nil {
			return "", fieldErrorf(field+".config", "%v", err)
		}
		source, key = s.Path, AgentSkillName(s)
		if _, present := n.Config["name"]; present && s.Name == "" {
			return "", fieldErrorf(field+".config.name", "omit name to use the source basename; an explicit name must not be empty or null")
		}
		if !agentSkillName.MatchString(key) || len(key) > 255 {
			return "", fieldErrorf(field+".config.name", "effective skill name %q must match %s and be at most 255 bytes", key, agentSkillName)
		}
	} else {
		var s AgentSkillsDirectory
		if err := DecodeCapabilityConfig(n, &s); err != nil {
			return "", fieldErrorf(field+".config", "%v", err)
		}
		source, key = s.Path, s.Path
	}
	if source == "/" || !canonicalAbsPath(source) {
		return "", fieldErrorf(field+".config.path", "skill path %q must be absolute and canonical, not the root", source)
	}
	return key, nil
}

// AgentSkillRequestsOf reads selected, flat declarations in contribution order.
// It preserves optionality so a runtime can refuse or record unavailable skills.
func AgentSkillRequestsOf(capabilities []Capability) ([]AgentSkillCapability, error) {
	var out []AgentSkillCapability
	for _, c := range capabilities {
		if c.Group != nil {
			return nil, fmt.Errorf("agent skills: select groups first")
		}
		if c.Type != CapabilityAgentSkill {
			continue
		}
		var s AgentSkill
		if err := DecodeCapabilityConfig(c, &s); err != nil {
			return nil, err
		}
		out = append(out, AgentSkillCapability{AgentSkill: s, Optional: c.Optional, Name: c.Name, Description: c.Description})
	}
	return out, nil
}

// AgentSkillsDirectoriesOf reads selected destinations without inferring host access.
func AgentSkillsDirectoriesOf(capabilities []Capability) ([]AgentSkillsDirectoryCapability, error) {
	var out []AgentSkillsDirectoryCapability
	for _, c := range capabilities {
		if c.Group != nil {
			return nil, fmt.Errorf("skill directories: select groups first")
		}
		if c.Type != CapabilityAgentSkillsDirectory {
			continue
		}
		var s AgentSkillsDirectory
		if err := DecodeCapabilityConfig(c, &s); err != nil {
			return nil, err
		}
		out = append(out, AgentSkillsDirectoryCapability{AgentSkillsDirectory: s, Optional: c.Optional, Name: c.Name, Description: c.Description})
	}
	return out, nil
}

// A source is image inventory, so its location must be known when the
// artifact is checked, even if its discovery name is chosen at create.
func validatePublishedSkillPaths(items []Capability, prefix string) error {
	var errs ValidationErrors
	for i, c := range items {
		field := fmt.Sprintf("%s[%d]", prefix, i)
		if c.Group != nil {
			errs.add(validatePublishedSkillPaths(c.Group.Capabilities, field+".group.capabilities"))
		} else if c.Type == CapabilityAgentSkill {
			if source, ok := c.Config["path"].(string); ok && ContainsArgRef(source) {
				errs.add(fieldErrorf(field+".config.path", "published skill source path must be literal; use a build-phase argument for image content"))
			}
		}
	}
	return errs.err()
}
