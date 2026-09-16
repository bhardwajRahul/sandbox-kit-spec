# The hardened busybox runs as nonroot, so the script arrives by COPY
# rather than a RUN that would need write access to /usr/local/bin — and
# --chmod sets the bit without a shell in the picture at all.
FROM dhi.io/busybox:1.37-debian13
COPY --chmod=755 <<'EOF' /usr/local/bin/tool
#!/bin/sh
echo tool v1
EOF
