package main

import (
	"testing"

	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestConfigDelta(t *testing.T) {
	lower := ocispecs.ImageConfig{
		Env: []string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"DEBIAN_FRONTEND=noninteractive",
			"LANG=C.UTF-8",
		},
		Labels:       map[string]string{"org.opencontainers.image.vendor": "debian"},
		ExposedPorts: map[string]struct{}{"22/tcp": {}},
		Volumes:      map[string]struct{}{"/var/lib/base": {}},
		Cmd:          []string{"bash"},
		User:         "root",
	}
	upper := ocispecs.ImageConfig{
		Env: []string{
			"PATH=/opt/tool/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"DEBIAN_FRONTEND=noninteractive",
			"LANG=en_US.UTF-8",
			"TOOL_HOME=/opt/tool",
		},
		Labels: map[string]string{
			"org.opencontainers.image.vendor": "debian",
			"tool.version":                    "1",
		},
		ExposedPorts: map[string]struct{}{"22/tcp": {}, "8080/tcp": {}},
		Volumes:      map[string]struct{}{"/var/lib/base": {}, "/var/cache/tool": {}},
		Entrypoint:   []string{"/opt/tool/bin/tool"},
		Cmd:          []string{"bash"},
		User:         "tool",
		WorkingDir:   "/opt/tool",
	}

	delta := configDelta(lower, upper)

	require.Equal(t, []string{
		"PATH=/opt/tool/bin",
		"LANG=en_US.UTF-8",
		"TOOL_HOME=/opt/tool",
	}, delta.Env, "PATH keeps only added elements; unchanged base env drops; changed and new vars survive")
	require.Equal(t, map[string]string{"tool.version": "1"}, delta.Labels)
	require.Equal(t, map[string]struct{}{"8080/tcp": {}}, delta.ExposedPorts)
	require.Equal(t, map[string]struct{}{"/var/cache/tool": {}}, delta.Volumes)
	require.Equal(t, []string{"/opt/tool/bin/tool"}, delta.Entrypoint, "recipe-set entrypoint recorded")
	require.Empty(t, delta.Cmd, "cmd identical to base is inherited, not stated")
	require.Equal(t, "tool", delta.User)
	require.Equal(t, "/opt/tool", delta.WorkingDir)
}

func TestConfigDeltaEmptyWhenRecipeAddsNothing(t *testing.T) {
	base := ocispecs.ImageConfig{
		Env:    []string{"PATH=/usr/bin", "LANG=C.UTF-8"},
		Labels: map[string]string{"vendor": "debian"},
	}
	delta := configDelta(base, base)
	require.Empty(t, delta.Env)
	require.Empty(t, delta.Labels)
	require.Empty(t, delta.ExposedPorts)
	require.Empty(t, delta.Volumes)
}

func TestConfigDeltaFromScratch(t *testing.T) {
	upper := ocispecs.ImageConfig{Env: []string{"PATH=/opt/bin", "FOO=bar"}}
	delta := configDelta(ocispecs.ImageConfig{}, upper)
	require.Equal(t, []string{"PATH=/opt/bin", "FOO=bar"}, delta.Env)
}

func TestFinalStage(t *testing.T) {
	t.Run("scratch final stage", func(t *testing.T) {
		got, err := finalStage([]byte(`
FROM debian:13 AS build
RUN make
FROM scratch
COPY --from=build /out /usr/local/bin
`), nil)
		require.NoError(t, err)
		require.Equal(t, stageBase{base: "scratch"}, got)
	})

	t.Run("external image base", func(t *testing.T) {
		got, err := finalStage([]byte("FROM busybox:1.37\nRUN touch /a\n"), nil)
		require.NoError(t, err)
		require.Equal(t, stageBase{base: "busybox:1.37"}, got)
	})

	t.Run("earlier stage base", func(t *testing.T) {
		got, err := finalStage([]byte(`
FROM debian:13 AS base
RUN apt-get update
FROM base
RUN apt-get install -y gh
`), nil)
		require.NoError(t, err)
		require.Equal(t, stageBase{base: "base", isStage: true}, got)
	})

	t.Run("arg-parameterized base resolves from meta-arg default", func(t *testing.T) {
		got, err := finalStage([]byte(`
ARG BASE=debian:13
FROM ${BASE}
RUN true
`), nil)
		require.NoError(t, err)
		require.Equal(t, stageBase{base: "debian:13"}, got)
	})

	t.Run("build-arg overrides meta-arg default", func(t *testing.T) {
		got, err := finalStage([]byte(`
ARG BASE=debian:13
FROM ${BASE}
RUN true
`), map[string]string{"build-arg:BASE": "alpine:3.21"})
		require.NoError(t, err)
		require.Equal(t, stageBase{base: "alpine:3.21"}, got)
	})

	t.Run("unresolvable base is an error", func(t *testing.T) {
		_, err := finalStage([]byte("ARG BASE\nFROM ${BASE}\nRUN true\n"), nil)
		require.ErrorContains(t, err, "does not resolve")
	})

	t.Run("stage name matching is case-insensitive", func(t *testing.T) {
		got, err := finalStage([]byte(`
FROM debian:13 AS Build
RUN make
FROM build
RUN true
`), nil)
		require.NoError(t, err)
		require.True(t, got.isStage)
	})
}
