package kit

import (
	"errors"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/stretchr/testify/require"
)

func TestBundledSkillArtifact(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		a := conforming(t)
		entry := spec.Capability{Type: spec.CapabilityAgentSkill, Config: map[string]any{"path": "/skills/review"}}
		if grouped {
			entry = spec.Capability{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{entry}}}
		}
		s := &state{artifact: a, descriptor: &spec.Descriptor{Capabilities: []spec.Capability{entry}}}
		require.NotEmpty(t, checkAgentSkillContent(t.Context(), s))
		a.files["/skills/review/SKILL.md"] = []byte("Review the changes.")
		require.Empty(t, checkAgentSkillContent(t.Context(), s))
		a.stats = map[string]FileStat{"/skills/review/SKILL.md": {Regular: false}}
		require.NotEmpty(t, checkAgentSkillContent(t.Context(), s))
		a.stats = nil
		a.readErrs = map[string]error{"/skills/review/SKILL.md": errors.New("unreadable")}
		require.NotEmpty(t, checkAgentSkillContent(t.Context(), s))
	}
}
