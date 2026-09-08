# The default image a Hearth workspace boots from. Deliberately fat for v1:
# the common toolchains are baked in so a fresh workspace is usable offline.
# Rebuild and publish via a `workspace-base-v<n>` git tag (see README.md).
FROM debian:12-slim

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    PATH=/usr/local/go/bin:/opt/rust/bin:/usr/local/bin:/usr/bin:/bin

ARG GO_VERSION=1.27.1
ARG NODE_VERSION=22
ARG RUST_VERSION=1.98.0
ARG TARGETARCH

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl wget git openssh-client \
      build-essential pkg-config \
      python3 python3-venv python3-pip \
      less vim-tiny nano ripgrep jq unzip \
 && rm -rf /var/lib/apt/lists/*

# Go
RUN arch="${TARGETARCH:-amd64}" \
 && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" -o /tmp/go.tgz \
 && tar -C /usr/local -xzf /tmp/go.tgz && rm /tmp/go.tgz \
 && go version

# Node.js (NodeSource)
RUN curl -fsSL "https://deb.nodesource.com/setup_${NODE_VERSION}.x" | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && rm -rf /var/lib/apt/lists/* \
 && node --version && npm --version

# Rust (system-wide, pinned)
ENV RUSTUP_HOME=/opt/rust CARGO_HOME=/opt/rust
RUN curl -fsSL https://sh.rustup.rs | sh -s -- -y \
      --no-modify-path --default-toolchain "${RUST_VERSION}" --profile minimal \
 && chmod -R a+rwX /opt/rust \
 && rustc --version

# Login shells reset PATH from /etc/profile, so put the toolchains back for
# interactive and `bash -l` sessions.
RUN printf 'export PATH=/usr/local/go/bin:/opt/rust/bin:$PATH\n' \
      > /etc/profile.d/hearth-toolchains.sh

# A non-root default user; the workspace volume mounts at /workspace.
# useradd lives in /usr/sbin, which is not on the curated PATH above.
RUN /usr/sbin/useradd -m -u 1000 -s /bin/bash hearth \
 && mkdir -p /workspace && chown hearth:hearth /workspace
WORKDIR /workspace
USER hearth

# Workspaces are driven over an exec channel, not a long-running command.
CMD ["sleep", "infinity"]
