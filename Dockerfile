# syntax=docker/dockerfile:1
FROM debian:bookworm-slim AS builder

ARG NODE_VERSION=22.14.0
ARG GO_VERSION=1.24.1

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    xz-utils \
    && rm -rf /var/lib/apt/lists/*

# 1. Node.js + pi coding agent
ENV PATH="/usr/local/node/bin:${PATH}"
RUN curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" \
    | tar -xJ -C /usr/local \
    && mv "/usr/local/node-v${NODE_VERSION}-linux-x64" /usr/local/node \
    && npm install -g @earendil-works/pi-coding-agent@0.74.2 \
    && npm cache clean --force

# 2. Go
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
    | tar -xz -C /usr/local

# 3. Rust (rustc + cargo, minimal profile)
ENV RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/usr/local/cargo
RUN curl -fsSL https://sh.rustup.rs | sh -s -- -y --profile minimal --no-modify-path

# 4. uv
RUN curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR="/usr/local/bin" sh

# 5. Patch @earendil-works/pi-coding-agent (stale extension ctx after reload/session replacement)
COPY patches/patch-pi-coding-agent.mjs /tmp/patch-pi-coding-agent.mjs
RUN node /tmp/patch-pi-coding-agent.mjs && rm -f /tmp/patch-pi-coding-agent.mjs

# -----------------------------------------------------------------------------
# Final minimal runtime stage (no apt/dpkg/curl/wget, non-root user)
# -----------------------------------------------------------------------------
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    git \
    ripgrep \
    gcc \
    libc6-dev \
    pkg-config \
    make \
    && rm -rf /var/lib/apt/lists/* \
    && rm -f /usr/bin/curl /usr/bin/wget \
    && rm -rf /var/cache/apt /var/lib/dpkg /usr/bin/apt* /usr/bin/dpkg*

COPY --from=builder /usr/local/node /usr/local/node
COPY --from=builder /usr/local/go /usr/local/go
COPY --from=builder /usr/local/rustup /usr/local/rustup
COPY --from=builder /usr/local/cargo /usr/local/cargo
COPY --from=builder /usr/local/bin/uv /usr/local/bin/uv
COPY --from=builder /usr/local/bin/uvx /usr/local/bin/uvx

RUN groupadd -g 1000 agent \
    && useradd -m -u 1000 -g 1000 -s /bin/bash agent \
    && mkdir -p /workspace /refs /home/agent/.pi/agent/sessions /home/agent/.cargo /home/agent/go /home/agent/.cache/uv /home/agent/.npm \
    && chown -R agent:agent /workspace /home/agent

ENV HOME=/home/agent \
    RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/home/agent/.cargo \
    GOPATH=/home/agent/go \
    UV_CACHE_DIR=/home/agent/.cache/uv \
    NPM_CONFIG_CACHE=/home/agent/.npm \
    PATH="/home/agent/.cargo/bin:/home/agent/go/bin:/usr/local/cargo/bin:/usr/local/go/bin:/usr/local/node/bin:/home/agent/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

USER agent
WORKDIR /workspace

ENTRYPOINT ["pi"]
