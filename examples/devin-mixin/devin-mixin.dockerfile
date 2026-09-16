# syntax=docker/dockerfile:1
# Cognition publishes no pinnable public install channel for the devin
# CLI, so the template image is the distribution: the overlay copies the
# binary the template ships. The kit's version: field speaks for the
# artifact, not for a binary version the channel cannot pin.
# The hardened dhi.io/sbx-templates mirror carries every other
# agent template but not devin, so this one stays on the public
# tag until it is mirrored.
FROM docker/sandbox-templates:devin-docker AS src

FROM scratch
COPY --from=src /usr/local/bin/devin /usr/local/bin/devin
