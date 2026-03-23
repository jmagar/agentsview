# PyPI Distribution for agentsview

**Date**: 2026-03-23
**Issue**: [#204](https://github.com/wesm/agentsview/issues/204)
**Status**: Draft

## Problem

Corporate environments often cannot download from GitHub releases
directly. Internal package registries (Artifactory, Nexus) mirror PyPI,
npm, and other standard registries. Publishing to PyPI lets these users
install through approved channels.

## Goal

`pip install agentsview` (or `pipx install agentsview`) installs the
agentsview binary and makes the `agentsview` CLI command available.

## Design

### Overview

Package the existing release binaries into platform-specific Python
wheels and publish them to PyPI alongside each GitHub release. No Python
source distribution (sdist) — the wheels contain the compiled Go binary
wrapped in a thin Python entry point.

### Components

#### 1. `scripts/build_wheels.py`

Standalone Python script (stdlib only, no external dependencies) that:

- Takes a directory of release archives
  (`agentsview_VERSION_OS_ARCH.tar.gz` / `.zip`)
- Extracts the binary from each archive
- Packages each into a properly-tagged Python wheel
- Writes wheels to an output directory

Interface:

```bash
python scripts/build_wheels.py \
  --version 0.15.0 \
  --input-dir artifacts/ \
  --output-dir wheels/
```

The script parses archive filenames to determine the target platform and
maps them to wheel platform tags.

#### 2. Wheel format (per platform)

Each wheel is a zip file named
`agentsview-{version}-py3-none-{platform_tag}.whl` containing:

```
agentsview/
  __init__.py          # get_binary_path(), main()
  __main__.py          # from . import main; main()
  bin/
    agentsview         # the Go binary (or agentsview.exe on Windows)
agentsview-{version}.dist-info/
  METADATA             # PyPI package metadata (PEP 566)
  WHEEL                # wheel format metadata (PEP 427)
  entry_points.txt     # console_scripts: agentsview = agentsview:main
  RECORD               # file checksums (path,sha256=BASE64,size)
```

The `py3-none-{platform_tag}` tag tells pip the wheel has no Python ABI
dependency and no Python version constraint — only the platform matters.

**Binary permissions**: The Go binary must have Unix executable
permissions (0755) set via `ZipInfo.external_attr` in the zip archive.
Without this, pip installs a non-executable binary.

**RECORD format**: Each line is `path,sha256=URLSAFE_BASE64,size`.
The RECORD file itself is listed as `RECORD,,` (no hash). See PEP 376.

**WHEEL file content**:

```
Wheel-Version: 1.0
Generator: agentsview-build-wheels
Root-Is-Purelib: false
Tag: py3-none-{platform_tag}
```

**METADATA required fields**: `Metadata-Version: 2.1`, `Name`,
`Version`, `Summary`, `Requires-Python: >=3.9`, `License`, `Home-page`,
`Description-Content-Type: text/markdown` (with README as long
description).

The Python wrapper (`__init__.py`) uses `os.execvp` on Unix (replacing
the Python process entirely) and `sys.exit(subprocess.call(...))` on
Windows to propagate the exit code.

#### 3. Release workflow changes

Add a `pypi` job to `.github/workflows/release.yml` that runs after the
existing `build` job:

1. Downloads all build artifacts (each in its own subdirectory:
   `agentsview-linux-amd64/`, `agentsview-darwin-arm64/`, etc.)
2. Runs `build_wheels.py` with `--input-dir artifacts/` — the script
   scans for `.tar.gz` and `.zip` archives, parses their filenames
   (e.g., `agentsview_0.15.0_linux_amd64.tar.gz`) to determine the
   platform, extracts the binary from each, and builds the wheel.
   Archives are never extracted into a flat directory — filenames carry
   the platform identity.
3. Publishes to PyPI using `pypa/gh-action-pypi-publish` with Trusted
   Publishers (OIDC)

The `pypi` job uses a GitHub environment named `pypi` for the OIDC
token exchange. It requires `id-token: write` permission for the OIDC
exchange (the existing top-level `contents: read` is insufficient).

#### 4. Per-platform smoke tests

Each build job runs `agentsview --version` on the freshly compiled
binary before archiving it. This catches obvious build failures (missing
libraries, wrong architecture, broken entry point) on all five
platforms — not just the one the `pypi` job runs on.

#### 5. Linux builds: manylinux_2_28 compatibility

The current release workflow builds Linux binaries on `ubuntu-latest`
(glibc 2.39). Binaries built on glibc 2.34+ require glibc 2.34+ at
runtime due to breaking symbol changes in that version. This excludes
RHEL 8 (glibc 2.28), which is widely deployed in enterprise environments
and supported through 2029.

To achieve `manylinux_2_28` compatibility, change the Linux build jobs to
use official PyPA manylinux containers:

- **x86_64**: `container: quay.io/pypa/manylinux_2_28_x86_64` on
  `ubuntu-latest`
- **arm64**: Cross-compile inside the same x86_64 container with
  `gcc-aarch64-linux-gnu` (matching the current cross-compilation
  pattern). If the cross-compiler is unavailable in AlmaLinux 8 repos,
  fall back to a native `ubuntu-24.04-arm` runner with
  `container: quay.io/pypa/manylinux_2_28_aarch64`.

`actions/setup-go` and `actions/setup-node` work inside these containers
(they download standalone binaries).

**Verification**: After building each Linux binary, check its glibc
version requirements with `objdump -T <binary> | grep GLIBC_ | sort -V`
and verify no symbol exceeds `GLIBC_2.28`. This runs as a build step
before the archive is created, failing the build if the binary would
violate the `manylinux_2_28` tag.

#### 6. macOS deployment target

The macOS builds must set `MACOSX_DEPLOYMENT_TARGET=11.0` during
compilation to ensure the binary is compatible with macOS 11+. Without
this, binaries built on macOS 15 runners may only work on macOS 15+,
making the `macosx_11_0` wheel tag incorrect.

**Verification**: After building each macOS binary, check the minimum OS
version in the Mach-O header with
`otool -l <binary> | grep -A3 LC_BUILD_VERSION` and verify the `minos`
field shows 11.0. This runs as a build step before archiving.

### Platform mapping

| Release archive suffix | Wheel platform tag            |
| ---------------------- | ----------------------------- |
| `linux_amd64`          | `manylinux_2_28_x86_64`      |
| `linux_arm64`          | `manylinux_2_28_aarch64`     |
| `darwin_amd64`         | `macosx_11_0_x86_64`         |
| `darwin_arm64`         | `macosx_11_0_arm64`          |
| `windows_amd64`        | `win_amd64`                  |

### PyPI authentication: Trusted Publishers (OIDC)

No API tokens or secrets needed. One-time manual setup:

1. Go to <https://pypi.org/manage/account/publishing/>
2. Add a pending publisher:
   - PyPI project name: `agentsview`
   - Owner: `wesm`
   - Repository name: `agentsview`
   - Workflow name: `release.yml`
   - Environment name: `pypi`
3. Create a GitHub environment named `pypi` in repo settings (Settings >
   Environments > New environment)

After the first successful publish, PyPI links the trusted publisher to
the package permanently.

### Version handling

The version tag (e.g., `v0.15.0`) has the `v` prefix stripped for PyPI
(becomes `0.15.0`). This matches PEP 440 conventions.

### Assumptions

- **Python runtime required**: `pip install agentsview` assumes the user
  has Python 3.9+ installed. This is not a standalone binary installer
  — it is a distribution channel for environments that already have
  Python and pip available (which is the norm in corporate environments
  using Artifactory/Nexus).
- **Unsupported platforms**: Users on platforms without a matching wheel
  (e.g., linux-arm32, FreeBSD) get pip's standard "no matching
  distribution found" error. No special handling needed — the GitHub
  releases page remains available for manual downloads.

## Out of scope

- **musl / Alpine wheels** — CGO binary links glibc; musl builds would
  require a separate C toolchain and are not in the current release
  matrix
- **windows-arm64** — not in current release matrix
- **sdist** — no Python source to build from
- **npm package** — can add later if requested
- **TestPyPI dry runs** — can add later for pre-release validation

## Prerequisites

- **PyPI package name**: Confirm `agentsview` is available on PyPI
  (verified 2026-03-23: `https://pypi.org/pypi/agentsview/json` returns
  404). Register the name by completing the trusted publisher setup
  before the first release with PyPI publishing.

## Implementation plan

1. Write `scripts/build_wheels.py`
2. Add tests for the wheel-building script
3. Update `.github/workflows/release.yml`:
   a. Change Linux build jobs to use manylinux_2_28 containers
   b. Add per-platform smoke tests and verification steps
   c. Add macOS `MACOSX_DEPLOYMENT_TARGET=11.0` and verification
   d. Add `pypi` job (download artifacts, build wheels, publish)
4. Manual setup: register trusted publisher on pypi.org, create `pypi`
   GitHub environment
5. Tag a release to test the full pipeline
