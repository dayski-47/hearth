# Workspace base image

This is the default image a Hearth workspace boots from. It is intentionally
large: the common toolchains are baked in so a workspace is usable straight away,
without reaching the network to install a compiler or a package manager.

## What is inside

| Component | Version / notes |
| --- | --- |
| Base | Debian 12 slim |
| Version control | git, openssh-client |
| C/C++ toolchain | build-essential, pkg-config |
| Go | 1.27.1, at `/usr/local/go` |
| Rust | 1.98.0, system-wide at `/opt/rust` (rustup + cargo) |
| Node.js | 22 with npm (NodeSource) |
| Python | 3.11 with venv and pip |
| Shell tools | ripgrep, jq, vim-tiny, nano, less, unzip, curl, wget |

The default user is `hearth` (uid 1000) and the workspace volume mounts at
`/workspace`.

## Build locally

```
just workspace-image
```

This tags the image `hearth-workspace-base:dev`. Point a dev gateway at it with:

```
HEARTH_WORKSPACE_IMAGE=hearth-workspace-base:dev
```

## Publish a new version

Bump the `ARG` versions in the Dockerfile if needed, commit, then:

```
git tag workspace-base-v<n>
git push origin workspace-base-v<n>
```

The `workspace-base` workflow builds the image, scans it with Trivy, and pushes
`ghcr.io/dayski-47/hearth-workspace-base:v<n>` and `:latest`.

## Using a different image

Any OCI image with a shell works. Set `HEARTH_WORKSPACE_IMAGE` to it. Hearth does
not require anything Hearth-specific in the image.
