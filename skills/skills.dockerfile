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

# Everything here except the kit's own three files, so adding a skill ships it
# without editing this recipe. Root-owned read-only reference material: no
# /home involvement at all, so none of the overlay ownership traps apply.
COPY . /out/usr/share/kit-skills
RUN set -eux; \
    cd /out/usr/share/kit-skills; \
    rm -f skills.yaml skills.dockerfile skills-context.md; \
    chown -R 0:0 /out; \
    test -f create-kit-v3/SKILL.md; \
    test -f migrate-kit-to-v3/SKILL.md

FROM scratch
COPY --from=stage /out /
