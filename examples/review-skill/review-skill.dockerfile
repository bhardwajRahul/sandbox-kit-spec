# syntax=docker/dockerfile:1
# Keep sources outside discovery directories, which the runtime assembles.
FROM scratch
COPY content /usr/share/review-skill/content
