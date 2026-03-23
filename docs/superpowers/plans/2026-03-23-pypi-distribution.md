# PyPI Distribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable `pip install agentsview` by packaging release binaries
into platform-specific Python wheels and publishing to PyPI.

**Architecture:** A standalone Python script (`scripts/build_wheels.py`)
takes the release archives already produced by CI, extracts each binary,
and wraps it in a properly-tagged wheel. A new `pypi` job in the release
workflow runs this script and publishes the wheels via Trusted Publishers
(OIDC). The Linux build jobs switch to manylinux_2_28 containers for
RHEL 8 compatibility.

**Tech Stack:** Python 3.9+ (stdlib only), GitHub Actions, PyPI Trusted
Publishers

**Spec:**
`docs/superpowers/specs/2026-03-23-pypi-distribution-design.md`

---

## File Structure

| File                                | Action | Purpose                          |
| ----------------------------------- | ------ | -------------------------------- |
| `scripts/build_wheels.py`           | Create | Wheel builder (stdlib only)      |
| `scripts/build_wheels_test.py`      | Create | Tests for the wheel builder      |
| `.github/workflows/release.yml`     | Modify | Add manylinux, smoke tests, pypi |

---

## Task 1: Wheel builder core — platform mapping and archive scanning

**Files:**
- Create: `scripts/build_wheels.py`
- Create: `scripts/build_wheels_test.py`

This task builds the foundation: constants, CLI argument parsing, and
the function that scans an input directory for release archives and
extracts the platform from each filename.

- [ ] **Step 1: Write test for platform mapping**

```python
# scripts/build_wheels_test.py
import pytest
from build_wheels import PLATFORM_MAP, parse_archive_filename


class TestPlatformMap:
    def test_all_platforms_have_required_fields(self):
        for key, val in PLATFORM_MAP.items():
            assert "wheel_tag" in val
            assert "binary_name" in val

    def test_linux_amd64(self):
        assert PLATFORM_MAP["linux_amd64"]["wheel_tag"] == (
            "manylinux_2_28_x86_64"
        )

    def test_windows_amd64(self):
        assert PLATFORM_MAP["windows_amd64"]["wheel_tag"] == "win_amd64"
        assert PLATFORM_MAP["windows_amd64"]["binary_name"] == (
            "agentsview.exe"
        )


class TestParseArchiveFilename:
    def test_tar_gz(self):
        result = parse_archive_filename(
            "agentsview_0.15.0_linux_amd64.tar.gz"
        )
        assert result == ("linux_amd64", "0.15.0")

    def test_zip(self):
        result = parse_archive_filename(
            "agentsview_0.15.0_windows_amd64.zip"
        )
        assert result == ("windows_amd64", "0.15.0")

    def test_darwin_arm64(self):
        result = parse_archive_filename(
            "agentsview_1.2.3_darwin_arm64.tar.gz"
        )
        assert result == ("darwin_arm64", "1.2.3")

    def test_unrecognized_returns_none(self):
        assert parse_archive_filename("SHA256SUMS") is None
        assert parse_archive_filename("random.tar.gz") is None
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd scripts && python -m pytest build_wheels_test.py -v
```

Expected: `ModuleNotFoundError` — `build_wheels` does not exist yet.

- [ ] **Step 3: Write platform mapping and filename parser**

```python
# scripts/build_wheels.py
"""Build platform-specific Python wheels from agentsview release archives.

Standalone script — stdlib only, no external dependencies.
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

PLATFORM_MAP: dict[str, dict[str, str]] = {
    "linux_amd64": {
        "wheel_tag": "manylinux_2_28_x86_64",
        "binary_name": "agentsview",
    },
    "linux_arm64": {
        "wheel_tag": "manylinux_2_28_aarch64",
        "binary_name": "agentsview",
    },
    "darwin_amd64": {
        "wheel_tag": "macosx_11_0_x86_64",
        "binary_name": "agentsview",
    },
    "darwin_arm64": {
        "wheel_tag": "macosx_11_0_arm64",
        "binary_name": "agentsview",
    },
    "windows_amd64": {
        "wheel_tag": "win_amd64",
        "binary_name": "agentsview.exe",
    },
}

_ARCHIVE_RE = re.compile(
    r"^agentsview_([^_]+)_(\w+_\w+)\.(tar\.gz|zip)$"
)


def parse_archive_filename(
    filename: str,
) -> tuple[str, str] | None:
    """Parse platform and version from an archive filename.

    Returns (platform_key, version) or None if unrecognized.
    """
    m = _ARCHIVE_RE.match(filename)
    if not m:
        return None
    version, platform_key, _ = m.groups()
    if platform_key not in PLATFORM_MAP:
        return None
    return platform_key, version
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd scripts && python -m pytest build_wheels_test.py -v
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add scripts/build_wheels.py scripts/build_wheels_test.py
git commit -m "feat(pypi): add platform mapping and archive filename parser"
```

---

## Task 2: Wheel builder — archive extraction

**Files:**
- Modify: `scripts/build_wheels.py`
- Modify: `scripts/build_wheels_test.py`

Add the function that extracts a Go binary from a `.tar.gz` or `.zip`
archive.

- [ ] **Step 1: Write tests for archive extraction**

```python
# Add to scripts/build_wheels_test.py
import io
import tarfile
import zipfile
from pathlib import Path

from build_wheels import extract_binary


class TestExtractBinary:
    def test_extract_from_tar_gz(self, tmp_path: Path):
        # Create a fake tar.gz with a binary inside
        archive_path = tmp_path / "agentsview_1.0.0_linux_amd64.tar.gz"
        with tarfile.open(archive_path, "w:gz") as tf:
            data = b"fake-binary-content"
            info = tarfile.TarInfo(name="agentsview")
            info.size = len(data)
            tf.addfile(info, io.BytesIO(data))

        result = extract_binary(archive_path, "agentsview")
        assert result == b"fake-binary-content"

    def test_extract_from_zip(self, tmp_path: Path):
        archive_path = tmp_path / "agentsview_1.0.0_windows_amd64.zip"
        with zipfile.ZipFile(archive_path, "w") as zf:
            zf.writestr("agentsview.exe", b"fake-exe-content")

        result = extract_binary(archive_path, "agentsview.exe")
        assert result == b"fake-exe-content"

    def test_missing_binary_raises(self, tmp_path: Path):
        archive_path = tmp_path / "agentsview_1.0.0_linux_amd64.tar.gz"
        with tarfile.open(archive_path, "w:gz") as tf:
            data = b"wrong-file"
            info = tarfile.TarInfo(name="other_file")
            info.size = len(data)
            tf.addfile(info, io.BytesIO(data))

        with pytest.raises(FileNotFoundError):
            extract_binary(archive_path, "agentsview")
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd scripts && python -m pytest build_wheels_test.py::TestExtractBinary -v
```

Expected: `ImportError` — `extract_binary` not defined.

- [ ] **Step 3: Implement extract_binary**

Add to `scripts/build_wheels.py`:

```python
import tarfile
import zipfile


def extract_binary(archive_path: Path, binary_name: str) -> bytes:
    """Extract a named binary from a .tar.gz or .zip archive.

    Returns the binary content as bytes.
    Raises FileNotFoundError if the binary is not in the archive.
    """
    name = str(archive_path)
    if name.endswith(".tar.gz"):
        with tarfile.open(archive_path, "r:gz") as tf:
            for member in tf.getmembers():
                if member.name == binary_name or member.name.endswith(
                    f"/{binary_name}"
                ):
                    f = tf.extractfile(member)
                    if f is None:
                        continue
                    return f.read()
    elif name.endswith(".zip"):
        with zipfile.ZipFile(archive_path, "r") as zf:
            for info in zf.infolist():
                if info.filename == binary_name or info.filename.endswith(
                    f"/{binary_name}"
                ):
                    return zf.read(info.filename)

    raise FileNotFoundError(
        f"{binary_name} not found in {archive_path.name}"
    )
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd scripts && python -m pytest build_wheels_test.py -v
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add scripts/build_wheels.py scripts/build_wheels_test.py
git commit -m "feat(pypi): add archive extraction for tar.gz and zip"
```

---

## Task 3: Wheel builder — wheel assembly

**Files:**
- Modify: `scripts/build_wheels.py`
- Modify: `scripts/build_wheels_test.py`

The core: assemble a valid wheel file from a binary blob. This includes
generating `__init__.py`, `__main__.py`, METADATA, WHEEL,
entry_points.txt, and RECORD, then packing them into a zip with the
correct filename and binary permissions.

- [ ] **Step 1: Write test for wheel assembly**

```python
# Add to scripts/build_wheels_test.py
import stat

from build_wheels import build_wheel


class TestBuildWheel:
    def test_produces_correctly_named_wheel(self, tmp_path: Path):
        wheel_path = build_wheel(
            binary_content=b"fake-binary",
            output_dir=tmp_path,
            version="1.2.3",
            platform_key="linux_amd64",
        )
        assert wheel_path.name == (
            "agentsview-1.2.3-py3-none-manylinux_2_28_x86_64.whl"
        )
        assert wheel_path.exists()

    def test_wheel_is_valid_zip(self, tmp_path: Path):
        wheel_path = build_wheel(
            binary_content=b"fake-binary",
            output_dir=tmp_path,
            version="1.0.0",
            platform_key="darwin_arm64",
        )
        assert zipfile.is_zipfile(wheel_path)

    def test_wheel_contains_expected_files(self, tmp_path: Path):
        wheel_path = build_wheel(
            binary_content=b"fake-binary",
            output_dir=tmp_path,
            version="1.0.0",
            platform_key="linux_amd64",
        )
        with zipfile.ZipFile(wheel_path) as zf:
            names = zf.namelist()
            assert "agentsview/__init__.py" in names
            assert "agentsview/__main__.py" in names
            assert "agentsview/bin/agentsview" in names
            assert "agentsview-1.0.0.dist-info/METADATA" in names
            assert "agentsview-1.0.0.dist-info/WHEEL" in names
            assert "agentsview-1.0.0.dist-info/entry_points.txt" in names
            assert "agentsview-1.0.0.dist-info/RECORD" in names

    def test_binary_has_executable_permissions(self, tmp_path: Path):
        wheel_path = build_wheel(
            binary_content=b"fake-binary",
            output_dir=tmp_path,
            version="1.0.0",
            platform_key="linux_amd64",
        )
        with zipfile.ZipFile(wheel_path) as zf:
            info = zf.getinfo("agentsview/bin/agentsview")
            unix_mode = (info.external_attr >> 16) & 0o777
            assert unix_mode & stat.S_IXUSR, "binary must be executable"

    def test_windows_wheel_uses_exe_name(self, tmp_path: Path):
        wheel_path = build_wheel(
            binary_content=b"fake-exe",
            output_dir=tmp_path,
            version="1.0.0",
            platform_key="windows_amd64",
        )
        with zipfile.ZipFile(wheel_path) as zf:
            assert "agentsview/bin/agentsview.exe" in zf.namelist()

    def test_metadata_contains_required_fields(self, tmp_path: Path):
        wheel_path = build_wheel(
            binary_content=b"fake",
            output_dir=tmp_path,
            version="2.0.0",
            platform_key="darwin_amd64",
        )
        with zipfile.ZipFile(wheel_path) as zf:
            metadata = zf.read(
                "agentsview-2.0.0.dist-info/METADATA"
            ).decode()
            assert "Metadata-Version: 2.1" in metadata
            assert "Name: agentsview" in metadata
            assert "Version: 2.0.0" in metadata
            assert "Requires-Python: >=3.9" in metadata

    def test_record_has_hashes_for_all_files(self, tmp_path: Path):
        wheel_path = build_wheel(
            binary_content=b"fake",
            output_dir=tmp_path,
            version="1.0.0",
            platform_key="linux_amd64",
        )
        with zipfile.ZipFile(wheel_path) as zf:
            record = zf.read(
                "agentsview-1.0.0.dist-info/RECORD"
            ).decode()
            # RECORD itself has no hash
            assert "RECORD,," in record
            # All other files have sha256 hashes
            for name in zf.namelist():
                if name.endswith("RECORD"):
                    continue
                assert name in record
                # Find the line and check it has a hash
                for line in record.splitlines():
                    if line.startswith(name + ","):
                        assert "sha256=" in line
                        break

    def test_entry_points(self, tmp_path: Path):
        wheel_path = build_wheel(
            binary_content=b"fake",
            output_dir=tmp_path,
            version="1.0.0",
            platform_key="linux_amd64",
        )
        with zipfile.ZipFile(wheel_path) as zf:
            ep = zf.read(
                "agentsview-1.0.0.dist-info/entry_points.txt"
            ).decode()
            assert "agentsview = agentsview:main" in ep
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd scripts && python -m pytest build_wheels_test.py::TestBuildWheel -v
```

Expected: `ImportError` — `build_wheel` not defined.

- [ ] **Step 3: Implement build_wheel**

Add to `scripts/build_wheels.py`:

```python
import base64
import csv
import hashlib
import io
import stat


def _compute_hash(data: bytes) -> str:
    """SHA-256 hash in wheel RECORD format (urlsafe base64, no padding)."""
    digest = hashlib.sha256(data).digest()
    return "sha256=" + (
        base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")
    )


def _generate_init_py(version: str, binary_name: str) -> str:
    return f'''\
"""agentsview — Go binary packaged as a Python wheel."""

import os
import stat
import subprocess
import sys

__version__ = "{version}"


def get_binary_path():
    """Return the path to the bundled binary."""
    binary = os.path.join(os.path.dirname(__file__), "bin", "{binary_name}")
    if sys.platform != "win32":
        mode = os.stat(binary).st_mode
        if not (mode & stat.S_IXUSR):
            os.chmod(
                binary, mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH
            )
    return binary


def main():
    """Execute the bundled binary."""
    binary = get_binary_path()
    if sys.platform == "win32":
        sys.exit(subprocess.call([binary] + sys.argv[1:]))
    else:
        os.execvp(binary, [binary] + sys.argv[1:])
'''


def _generate_main_py() -> str:
    return "from . import main\nmain()\n"


def _generate_metadata(version: str, readme: str | None = None) -> str:
    lines = [
        "Metadata-Version: 2.1",
        "Name: agentsview",
        f"Version: {version}",
        "Summary: Local web viewer for AI agent sessions",
        "Home-page: https://github.com/wesm/agentsview",
        "Author: Wes McKinney",
        "License: MIT",
        "Requires-Python: >=3.9",
    ]
    if readme:
        lines.append("Description-Content-Type: text/markdown")
        lines.append("")
        lines.append(readme)
    return "\n".join(lines) + "\n"


def _generate_wheel_metadata(platform_tag: str) -> str:
    return (
        "Wheel-Version: 1.0\n"
        "Generator: agentsview-build-wheels\n"
        "Root-Is-Purelib: false\n"
        f"Tag: py3-none-{platform_tag}\n"
    )


def _generate_entry_points() -> str:
    return "[console_scripts]\nagentsview = agentsview:main\n"


def _generate_record(files: dict[str, bytes]) -> str:
    buf = io.StringIO()
    writer = csv.writer(buf)
    for path, content in files.items():
        if path.endswith("RECORD"):
            writer.writerow([path, "", ""])
        else:
            writer.writerow([path, _compute_hash(content), len(content)])
    return buf.getvalue()


def build_wheel(
    binary_content: bytes,
    output_dir: Path,
    version: str,
    platform_key: str,
    readme: str | None = None,
) -> Path:
    """Build a single platform-specific wheel.

    Returns the path to the created .whl file.
    """
    plat = PLATFORM_MAP[platform_key]
    wheel_tag = plat["wheel_tag"]
    binary_name = plat["binary_name"]

    dist_info = f"agentsview-{version}.dist-info"

    files: dict[str, bytes] = {}
    files["agentsview/__init__.py"] = _generate_init_py(
        version, binary_name
    ).encode()
    files["agentsview/__main__.py"] = _generate_main_py().encode()
    files[f"agentsview/bin/{binary_name}"] = binary_content

    files[f"{dist_info}/METADATA"] = _generate_metadata(
        version, readme
    ).encode()
    files[f"{dist_info}/WHEEL"] = _generate_wheel_metadata(
        wheel_tag
    ).encode()
    files[f"{dist_info}/entry_points.txt"] = (
        _generate_entry_points().encode()
    )

    # RECORD must be last — it includes hashes of all other files
    record_path = f"{dist_info}/RECORD"
    files[record_path] = b""  # placeholder
    files[record_path] = _generate_record(files).encode()

    wheel_name = f"agentsview-{version}-py3-none-{wheel_tag}.whl"
    wheel_path = output_dir / wheel_name

    with zipfile.ZipFile(wheel_path, "w", zipfile.ZIP_DEFLATED) as whl:
        for file_path, content in files.items():
            info = zipfile.ZipInfo(file_path)
            if "/bin/" in file_path:
                # rwxr-xr-x (0755)
                info.external_attr = (
                    stat.S_IRWXU
                    | stat.S_IRGRP
                    | stat.S_IXGRP
                    | stat.S_IROTH
                    | stat.S_IXOTH
                ) << 16
            whl.writestr(info, content)

    return wheel_path
```

- [ ] **Step 4: Run all tests to verify they pass**

```bash
cd scripts && python -m pytest build_wheels_test.py -v
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add scripts/build_wheels.py scripts/build_wheels_test.py
git commit -m "feat(pypi): implement wheel assembly with metadata and RECORD"
```

---

## Task 4: Wheel builder — CLI and end-to-end flow

**Files:**
- Modify: `scripts/build_wheels.py`
- Modify: `scripts/build_wheels_test.py`

Wire up the CLI (`--version`, `--input-dir`, `--output-dir`) and the
top-level `build_all_wheels` function that scans a directory, extracts
binaries, and produces wheels. Add an end-to-end test with fake
archives.

- [ ] **Step 1: Write end-to-end test**

```python
# Add to scripts/build_wheels_test.py
from build_wheels import build_all_wheels


class TestBuildAllWheels:
    def _make_archive(
        self, directory: Path, version: str, goos: str, goarch: str
    ):
        """Create a fake release archive matching CI naming."""
        binary_name = "agentsview"
        if goos == "windows":
            binary_name = "agentsview.exe"
            archive = directory / f"agentsview_{version}_{goos}_{goarch}.zip"
            with zipfile.ZipFile(archive, "w") as zf:
                zf.writestr(binary_name, f"fake-{goos}-{goarch}")
        else:
            archive = (
                directory / f"agentsview_{version}_{goos}_{goarch}.tar.gz"
            )
            with tarfile.open(archive, "w:gz") as tf:
                data = f"fake-{goos}-{goarch}".encode()
                info = tarfile.TarInfo(name=binary_name)
                info.size = len(data)
                tf.addfile(info, io.BytesIO(data))
        return archive

    def test_builds_all_wheels(self, tmp_path: Path):
        input_dir = tmp_path / "artifacts"
        input_dir.mkdir()
        output_dir = tmp_path / "wheels"
        output_dir.mkdir()

        platforms = [
            ("linux", "amd64"),
            ("linux", "arm64"),
            ("darwin", "amd64"),
            ("darwin", "arm64"),
            ("windows", "amd64"),
        ]
        for goos, goarch in platforms:
            self._make_archive(input_dir, "1.0.0", goos, goarch)

        # Add a non-archive file to verify it's skipped
        (input_dir / "SHA256SUMS").write_text("deadbeef")

        wheels = build_all_wheels(
            input_dir=input_dir,
            output_dir=output_dir,
            version="1.0.0",
        )
        assert len(wheels) == 5

        wheel_names = {w.name for w in wheels}
        assert "agentsview-1.0.0-py3-none-manylinux_2_28_x86_64.whl" in (
            wheel_names
        )
        assert "agentsview-1.0.0-py3-none-win_amd64.whl" in wheel_names
        assert "agentsview-1.0.0-py3-none-macosx_11_0_arm64.whl" in (
            wheel_names
        )

    def test_skips_unknown_archives(self, tmp_path: Path):
        input_dir = tmp_path / "in"
        input_dir.mkdir()
        output_dir = tmp_path / "out"
        output_dir.mkdir()

        # Unknown platform
        archive = input_dir / "agentsview_1.0.0_freebsd_amd64.tar.gz"
        with tarfile.open(archive, "w:gz") as tf:
            data = b"fake"
            info = tarfile.TarInfo(name="agentsview")
            info.size = len(data)
            tf.addfile(info, io.BytesIO(data))

        wheels = build_all_wheels(
            input_dir=input_dir,
            output_dir=output_dir,
            version="1.0.0",
        )
        assert len(wheels) == 0

    def test_version_override(self, tmp_path: Path):
        """--version overrides the version in the archive filename."""
        input_dir = tmp_path / "in"
        input_dir.mkdir()
        output_dir = tmp_path / "out"
        output_dir.mkdir()
        self._make_archive(input_dir, "0.15.0", "linux", "amd64")

        wheels = build_all_wheels(
            input_dir=input_dir,
            output_dir=output_dir,
            version="99.0.0",
        )
        assert len(wheels) == 1
        assert "99.0.0" in wheels[0].name
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd scripts && python -m pytest build_wheels_test.py::TestBuildAllWheels -v
```

Expected: `ImportError` — `build_all_wheels` not defined.

- [ ] **Step 3: Implement build_all_wheels and CLI**

Add to `scripts/build_wheels.py`:

```python
def build_all_wheels(
    input_dir: Path,
    output_dir: Path,
    version: str,
    readme: str | None = None,
) -> list[Path]:
    """Scan input_dir for release archives and build a wheel for each."""
    output_dir.mkdir(parents=True, exist_ok=True)
    wheels: list[Path] = []

    for archive_path in sorted(input_dir.iterdir()):
        if not archive_path.is_file():
            continue
        parsed = parse_archive_filename(archive_path.name)
        if parsed is None:
            continue
        platform_key, _ = parsed
        binary_name = PLATFORM_MAP[platform_key]["binary_name"]

        binary_content = extract_binary(archive_path, binary_name)
        wheel_path = build_wheel(
            binary_content=binary_content,
            output_dir=output_dir,
            version=version,
            platform_key=platform_key,
            readme=readme,
        )
        wheels.append(wheel_path)
        print(f"  {wheel_path.name}")

    return wheels


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Build Python wheels from agentsview release archives",
    )
    parser.add_argument(
        "--version", required=True, help="Package version (e.g. 0.15.0)"
    )
    parser.add_argument(
        "--input-dir",
        required=True,
        type=Path,
        help="Directory containing release archives",
    )
    parser.add_argument(
        "--output-dir",
        default=Path("./dist"),
        type=Path,
        help="Directory for built wheels (default: ./dist)",
    )
    parser.add_argument(
        "--readme",
        type=Path,
        help="Path to README.md for PyPI long description",
    )
    args = parser.parse_args()

    if not args.input_dir.is_dir():
        print(f"Error: {args.input_dir} is not a directory", file=sys.stderr)
        return 1

    readme_content = None
    if args.readme:
        readme_content = args.readme.read_text(encoding="utf-8")

    print(f"Building wheels v{args.version} from {args.input_dir}")
    wheels = build_all_wheels(
        input_dir=args.input_dir,
        output_dir=args.output_dir,
        version=args.version,
        readme=readme_content,
    )

    if not wheels:
        print("Error: no wheels were built", file=sys.stderr)
        return 1

    print(f"\nBuilt {len(wheels)} wheel(s) in {args.output_dir}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
```

- [ ] **Step 4: Run all tests to verify they pass**

```bash
cd scripts && python -m pytest build_wheels_test.py -v
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add scripts/build_wheels.py scripts/build_wheels_test.py
git commit -m "feat(pypi): add CLI and end-to-end build_all_wheels"
```

---

## Task 5: Release workflow — manylinux containers and verification

**Files:**
- Modify: `.github/workflows/release.yml`

Switch the Linux build jobs to manylinux_2_28 containers, add
per-platform smoke tests, add glibc verification for Linux, and add
macOS deployment target + verification.

- [ ] **Step 1: Add container and smoke tests to build matrix**

In `.github/workflows/release.yml`, update the matrix to add container
images for Linux and add the `MACOSX_DEPLOYMENT_TARGET` env var for
macOS:

```yaml
    strategy:
      fail-fast: false
      matrix:
        include:
          - os: ubuntu-latest
            goos: linux
            goarch: amd64
            container: quay.io/pypa/manylinux_2_28_x86_64
          - os: ubuntu-latest
            goos: linux
            goarch: arm64
            cc: aarch64-linux-gnu-gcc
            container: quay.io/pypa/manylinux_2_28_x86_64
          - os: macos-15
            goos: darwin
            goarch: amd64
          - os: macos-15
            goos: darwin
            goarch: arm64
          - os: windows-latest
            goos: windows
            goarch: amd64

    runs-on: ${{ matrix.os }}
    container: ${{ matrix.container || '' }}
```

- [ ] **Step 2: Update cross-compiler install step for manylinux**

Replace the `apt-get` step with `yum` for the manylinux container
(AlmaLinux 8 uses `yum`/`dnf`):

```yaml
      - name: Install cross-compiler (Linux ARM64)
        if: matrix.goarch == 'arm64' && matrix.goos == 'linux'
        run: dnf install -y gcc-aarch64-linux-gnu binutils-aarch64-linux-gnu
```

Note: if `gcc-aarch64-linux-gnu` is not available in AlmaLinux 8 repos,
this step will fail and we fall back to the native arm64 runner approach
(see spec). In that case, replace the arm64 matrix entry with:

```yaml
          - os: ubuntu-24.04-arm
            goos: linux
            goarch: arm64
            container: quay.io/pypa/manylinux_2_28_aarch64
```

- [ ] **Step 3: Add MACOSX_DEPLOYMENT_TARGET to the Build step**

In the Build step's `env:` block, add the deployment target
conditionally:

```yaml
      - name: Build
        shell: bash
        env:
          GOOS: ${{ matrix.goos }}
          GOARCH: ${{ matrix.goarch }}
          CGO_ENABLED: "1"
          CC: ${{ matrix.cc || '' }}
          MACOSX_DEPLOYMENT_TARGET: ${{ matrix.goos == 'darwin' && '11.0' || '' }}
```

- [ ] **Step 4: Add smoke test and verification steps**

The existing Build step deletes the binary after archiving (`rm
agentsview${EXT}`). Move the smoke test and verification into the Build
step's `run:` block, BEFORE the archive-and-delete logic. Insert these
lines after the `go build` command and before `cd dist`:

```yaml
          # Smoke test (skip cross-compiled binaries that can't run on host)
          if [ "$GOOS" = "$(go env GOHOSTOS)" ] && [ "$GOARCH" = "$(go env GOHOSTARCH)" ]; then
            ./dist/agentsview${EXT} --version
          fi

          # Verify glibc compatibility (Linux)
          if [ "$GOOS" = "linux" ]; then
            MAX_GLIBC="GLIBC_2.28"
            HIGHEST=$(objdump -T dist/agentsview | grep -oP 'GLIBC_\d+\.\d+' | sort -t. -k1,1n -k2,2n | tail -1)
            echo "Highest glibc symbol: $HIGHEST (max allowed: $MAX_GLIBC)"
            if [ "$(printf '%s\n%s' "$MAX_GLIBC" "$HIGHEST" | sort -t. -k1,1n -k2,2n | tail -1)" != "$MAX_GLIBC" ]; then
              echo "ERROR: Binary requires $HIGHEST but wheel claims $MAX_GLIBC"
              exit 1
            fi
          fi

          # Verify macOS deployment target
          if [ "$GOOS" = "darwin" ]; then
            MINOS=$(otool -l dist/agentsview | grep -A3 LC_BUILD_VERSION | grep minos | awk '{print $2}')
            echo "Minimum macOS version: $MINOS"
            if [ "$MINOS" != "11.0" ]; then
              echo "ERROR: Expected minos 11.0, got $MINOS"
              exit 1
            fi
          fi
```

These run inside the Build step before `cd dist` / `tar` / `rm`, so
the binary is still on disk. The smoke test uses `go env GOHOSTOS` and
`go env GOHOSTARCH` to skip cross-compiled binaries that can't execute
on the build host. The glibc and macOS checks work on any binary
regardless of architecture.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: switch Linux builds to manylinux_2_28, add verification steps"
```

---

## Task 6: Release workflow — PyPI publish job

**Files:**
- Modify: `.github/workflows/release.yml`

Add the `pypi` job that downloads artifacts, builds wheels, and
publishes to PyPI.

- [ ] **Step 1: Add pypi job**

Append to `.github/workflows/release.yml` after the `release` job:

```yaml
  pypi:
    needs: [build, release]
    runs-on: ubuntu-latest
    environment: pypi
    permissions:
      contents: read
      id-token: write
    steps:
      - uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd  # v6.0.2
        with:
          persist-credentials: false

      - uses: actions/setup-python@a309ff8b426b58ec0e2a45f0f869d46889d02405  # v6.2.0
        with:
          python-version: "3.12"

      - uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c  # v8.0.1
        with:
          path: artifacts
          merge-multiple: true

      - name: Build wheels
        run: |
          VERSION=${GITHUB_REF#refs/tags/v}
          python scripts/build_wheels.py \
            --version "$VERSION" \
            --input-dir artifacts \
            --output-dir wheels \
            --readme README.md

      - name: Smoke test wheel
        run: |
          VERSION=${GITHUB_REF#refs/tags/v}
          pip install wheels/agentsview-${VERSION}-py3-none-manylinux_2_28_x86_64.whl
          agentsview --version

      - name: Publish to PyPI
        uses: pypa/gh-action-pypi-publish@ed0c53931b1dc9bd32cbe73a98c7f6766f8a527e  # v1.13.0
        with:
          packages-dir: wheels/
```

- [ ] **Step 2: Verify the full workflow file is valid YAML**

```bash
python -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml'))"
```

If `pyyaml` is not installed, use:

```bash
python -c "
import json, sys
# Basic YAML check: ensure no syntax errors in key structure
content = open('.github/workflows/release.yml').read()
if 'pypi:' not in content:
    print('ERROR: pypi job not found', file=sys.stderr)
    sys.exit(1)
if 'id-token: write' not in content:
    print('ERROR: id-token permission not found', file=sys.stderr)
    sys.exit(1)
print('Workflow structure looks correct')
"
```

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: add PyPI publish job with Trusted Publishers"
```

---

## Task 7: Manual setup — PyPI Trusted Publisher and GitHub environment

This task is performed by the repository owner (not automated).

- [ ] **Step 1: Create GitHub environment**

Go to repository settings:
`https://github.com/wesm/agentsview/settings/environments`

Click "New environment", name it `pypi`, save. No protection rules are
needed for now (the release workflow already gates on tag pushes).

- [ ] **Step 2: Register trusted publisher on PyPI**

Go to: `https://pypi.org/manage/account/publishing/`

Under "Add a new pending publisher", fill in:

| Field             | Value         |
| ----------------- | ------------- |
| PyPI project name | `agentsview`  |
| Owner             | `wesm`        |
| Repository name   | `agentsview`  |
| Workflow name     | `release.yml` |
| Environment name  | `pypi`        |

Click "Add". PyPI will reserve the package name and authorize the
workflow to publish.

- [ ] **Step 3: Verify setup**

After completing both steps, the PyPI pending publishers page should
show `agentsview` linked to `wesm/agentsview` / `release.yml` /
`pypi`. The first successful release will convert the pending publisher
into an active publisher and create the actual package.
