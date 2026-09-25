## Sandbox Kit Spec dev tools

This sandbox carries the toolchain for working on the
`docker/sandbox-kit-spec` repository, on top of what the workload already
ships (Go, Docker with buildx, gh, git, jq, node):

- `task` — runs `Taskfile.yaml`; `task --list` names every target.
- `golangci-lint` — the version CI pins, so `task lint:go` judges as CI does.
- `regctl` — inspect registries: `regctl tag ls <repo>`, `regctl manifest get <ref>`.

Read `AGENTS.md` at the repository root before changing anything: it says
which targets verify which kind of change. The short form is `task validate`,
`task lint` and `task test:unit` for every change, `task test:tck` for
anything under `tck/` or `docs/spec/`, and `task kit:dev KIT=<name>` for a
frontend change.

Egress is granted for what those runs reach — the Go module proxy, Docker Hub
and dhi.io, and the release endpoints `task versions:check` reads. A kit
build (`task kit:dev`, `task kit:build`) downloads whatever that kit's recipe
names, which this policy does not cover; if one fails on a network refusal,
that is the boundary doing its job, and the fix is to declare the host in
the composition rather than to work around it.
