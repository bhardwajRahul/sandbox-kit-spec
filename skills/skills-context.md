## Kit authoring skills

This sandbox carries the Docker sandbox kit skills on disk at
`/usr/share/kit-skills/`. They are ordinary markdown — read them directly.

| Read this | When |
|---|---|
| `/usr/share/kit-skills/create-kit-v3/SKILL.md` | Writing a new kit. Covers workload vs mixin, the descriptor, capabilities, version pinning, and how to verify the result. |
| `/usr/share/kit-skills/create-kit-v3/RECIPES.md` | Writing a kit's content recipe, and the ownership rules an overlay has to satisfy. |
| `/usr/share/kit-skills/migrate-kit-to-v3/SKILL.md` | Porting a kit from the v2 `spec.yaml` grammar. |
| `/usr/share/kit-skills/migrate-kit-to-v3/FIELD-MAPPING.md` | The field-by-field v2 → v3 mapping, and the gotcha list. |

Start with the `SKILL.md` for the task at hand; each links to its own
reference files.

The skills link out to the spec itself — `docs/spec/SPEC-v3.md`, the
capability pages under `docs/spec/capabilities/`, `conformance.md`, and the
`examples/` kits. Those are not on disk, and this sandbox can reach exactly
one place to get them:

```sh
git clone https://github.com/docker/sandbox-kit-spec
```

Egress is bounded to that one repository, and to reads of it: nothing else on
github.com is reachable, and the grant covers fetching rather than pushing.

**This kit contributes no credential of its own**, so on a bare workload the
clone is unauthenticated: it works while the repository is public and fails on
authentication — not on egress — if it is not.

It may still end up authenticated, and that is not this kit's doing.
Credentials union across a composition, and an `inject` rule rewrites requests
at the outbound boundary by **domain** rather than by which process made them,
so another kit contributing a runtime GitHub credential covering `github.com`
authenticates this clone too. A git credential helper configured in the image
does the same. So treat authentication as a property of the composition you
are running in, not something to infer from this kit.

If the clone does fail, the files above remain authoritative for everything
they cover; what you lose is the material they link out to.

Follow the links when a skill defers to the spec rather than guessing at a
rule — the skills summarise, the spec decides.

They are not in an agent skills directory on purpose. A skills directory is
where the host mounts its own shared store, and content shipped there would be
hidden by that mount rather than merged with it — so these live at a path
nothing mounts over, and this note is how you find them.

Two things in them are worth knowing before you need them, because they are
the mistakes that cost the most time:

- **A build proves a recipe ran, not that a kit works.** For a mixin, compose
  the built overlay onto a bare base and run the tool. That is the only check
  that catches an overlay shipping a dangling symlink, which happens whenever
  an installer relocates a launcher without its payload.
- **An overlay states ownership for every directory level it ships**, and its
  entries override the base's. `/home` owned by uid 1000 hands away a
  directory the kit does not own; `/home/agent` owned by root takes `$HOME`
  from the agent user.
