# syntax=docker/dockerfile:1
# Three release binaries staged under /out and copied onto scratch, so the
# overlay lands on any base without a dpkg database or a shared-library
# closure to satisfy. Each is a static Go binary published per
# architecture under the GOARCH name, which is what TARGETARCH carries.
FROM dhi.io/debian-base:trixie-dev AS build
ARG TARGETARCH
ARG TASK_VERSION
ARG GOLANGCI_LINT_VERSION
ARG REGCTL_VERSION
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

# Every pin is a claim about content, so each step re-reads the version
# it installed and fails the build on a mismatch. The archive layouts
# differ: task ships the binary at the tarball root, golangci-lint under
# a versioned directory, regctl as a bare file.
RUN set -eux; \
    mkdir -p /out/usr/local/bin; \
    curl -fsSL "https://github.com/go-task/task/releases/download/v${TASK_VERSION}/task_linux_${TARGETARCH}.tar.gz" \
      | tar -xz -C /out/usr/local/bin task; \
    /out/usr/local/bin/task --version | grep -q "${TASK_VERSION}"

RUN set -eux; \
    curl -fsSL "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${TARGETARCH}.tar.gz" \
      | tar -xz -C /out/usr/local/bin --strip-components=1 \
          "golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${TARGETARCH}/golangci-lint"; \
    /out/usr/local/bin/golangci-lint version | grep -q "${GOLANGCI_LINT_VERSION}"

RUN set -eux; \
    curl -fsSL "https://github.com/regclient/regclient/releases/download/v${REGCTL_VERSION}/regctl-linux-${TARGETARCH}" \
      -o /out/usr/local/bin/regctl; \
    chmod 0755 /out/usr/local/bin/regctl; \
    /out/usr/local/bin/regctl version | grep -q "v${REGCTL_VERSION}"

# The release tarballs preserve the uid of the machine that packed them
# — 1001, a CI runner — and tar carries it in. On an unknown base that
# uid may be a real account, and a file's owner can rewrite it whatever
# its mode says, so the whole tree is handed to root, which is what a
# root-run install would have left anyway.
RUN chown -R 0:0 /out

# The workload sets GOPATH=/go but may not create it. Ship an agent-owned
# workspace so module downloads and go install work without root. Keep
# /out itself root-owned: its metadata becomes the overlay's root.
RUN mkdir -p /out/go/bin /out/go/pkg/mod \
 && chown -R 1000:1000 /out/go

# The overlay: three tools and a writable Go workspace, touching nothing
# under /home.
FROM scratch
COPY --from=build /out /
