FROM busybox:1.37 AS stage
COPY existing.skill /out/home/agent/.kit-tck/skills/review/SKILL.md
RUN chown -R 1000:1000 /out/home/agent
FROM scratch
COPY --from=stage /out /
