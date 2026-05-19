FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# Minimal toolchain the installer + sidekick install step need:
#   - curl, ca-certificates: fetch release tarball + checksums
#   - tar: extract the tarball
#   - bash: install.sh shebang
#   - jq: inspect agent settings JSON in T7 assertions
#   - git: `sidekick install` may shell out to git when wiring the skill
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash \
      ca-certificates \
      curl \
      git \
      jq \
      tar \
 && rm -rf /var/lib/apt/lists/*

# The installer prefers /usr/local/bin and falls back to ~/.local/bin. We
# run as root by default in the test (matches CI runner expectations), so
# /usr/local/bin is writable and the binary lands there.
ENV PATH=/usr/local/bin:/usr/bin:/bin

WORKDIR /work
