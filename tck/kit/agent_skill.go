package kit

import (
	"context"
	"path"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

func checkAgentSkillContent(ctx context.Context, s *state) []report.Finding {
	// Optional groups still publish their content; selection cannot excuse
	// a malformed artifact or make a missing skill appear later.
	skills, err := spec.AgentSkillRequestsOf(spec.DeclaredCapabilities(s.descriptor.Capabilities))
	if err != nil {
		return fail("decode bundled skills: %v", err)
	}
	for _, skill := range skills {
		file := path.Join(skill.Path, "SKILL.md")
		present, err := hasFile(ctx, s.artifact, file)
		if err != nil {
			return fail("read bundled skill %s: %v", file, err)
		}
		if !present {
			return fail("bundled skill %q does not carry %s", spec.AgentSkillName(skill.AgentSkill), file)
		}
		if source, ok := s.artifact.(statChecker); ok {
			stat, exists, err := source.FileStat(ctx, file)
			if err != nil {
				return fail("stat bundled skill %s: %v", file, err)
			}
			if !exists || !stat.Regular {
				return fail("bundled skill %s must be a regular file", file)
			}
		}
	}
	return nil
}
