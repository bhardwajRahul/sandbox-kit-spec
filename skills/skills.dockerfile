# syntax=docker/dockerfile:1
# The overlay: this directory's skills, and nothing else.
#
# The kit lives beside the content it ships rather than under examples/, which
# is what lets it follow the ordinary build convention — the descriptor's
# directory IS the build context, so `cd skills && docker buildx build . -f
# skills.yaml` works, and `contentFile:` resolves as a sibling. A kit under
# examples/ would have had to reach up to this directory, and both COPY sources
# and `contentFile` resolve against the context rather than the descriptor's
# own directory, so that build needed the repository root as its context and a
# `contentFile` spelled from there. One misplaced kit is a worse trade than one
# kit that is not an example.
FROM busybox:1.37 AS stage

# Everything the sibling .dockerignore does not exclude, so adding a skill
# ships it without editing this recipe. What is excluded and why is stated
# there — notably README.md, which documents host-side symlink wiring an agent
# reading it inside a sandbox would try to reproduce.
#
# COPY already lands files root-owned, so no chown: this is read-only
# reference material under /usr/share with no /home involvement at all, and
# none of the overlay ownership traps apply.
COPY . /out/usr/share/kit-skills

# Hard assertions, not `if [ -e ]`: an empty overlay is the failure this
# catches, and a conditional guard would pass on one.
RUN set -eux; \
    cd /out/usr/share/kit-skills; \
    test -f create-kit-v3/SKILL.md; \
    test -f migrate-kit-to-v3/SKILL.md; \
    test ! -e README.md; \
    test ! -e skills.yaml; \
    test ! -e .dockerignore

FROM scratch
COPY --from=stage /out /
