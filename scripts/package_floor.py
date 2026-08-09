#!/usr/bin/env python3
"""Build the inert, parameterized Syncthing compatibility-floor pak."""

from __future__ import annotations

import argparse
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import zipfile


ROOT = Path(__file__).resolve().parents[1]
VERSION_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+$")


def version(value: str) -> str:
    if not VERSION_RE.fullmatch(value) or any(
        int(component) > 9999 for component in value.split(".")
    ):
        raise argparse.ArgumentTypeError("must be an exact MAJOR.MINOR.PATCH")
    return value


def write_zip(package_dir: Path, archive_path: Path) -> None:
    archive_path.parent.mkdir(parents=True, exist_ok=True)
    archive_path.unlink(missing_ok=True)
    with zipfile.ZipFile(
        archive_path, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9
    ) as output:
        for path in sorted(package_dir.rglob("*")):
            if not path.is_file():
                continue
            relative = PurePosixPath(package_dir.name) / path.relative_to(package_dir)
            mode = 0o755 if path.stat().st_mode & stat.S_IXUSR else 0o644
            info = zipfile.ZipInfo(str(relative), date_time=(1980, 1, 1, 0, 0, 0))
            info.external_attr = (stat.S_IFREG | mode) << 16
            info.create_system = 3
            output.writestr(
                info,
                path.read_bytes(),
                compress_type=zipfile.ZIP_DEFLATED,
                compresslevel=9,
            )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--pak-version", type=version, default="0.0.1")
    parser.add_argument("--min-leaf-version", type=version, required=True)
    args = parser.parse_args()

    floor_binary = ROOT / "build" / "mlp1" / "bin" / "leaf-syncthing-floor"
    if not floor_binary.is_file():
        raise SystemExit(f"missing floor UI binary: {floor_binary}")

    package_dir = ROOT / "build" / "mlp1" / "floor" / "package" / "Syncthing.pak"
    archive_path = ROOT / "build" / "mlp1" / "floor" / "Syncthing.mlp1.pak.zip"
    shutil.rmtree(package_dir.parent, ignore_errors=True)
    (package_dir / "bin").mkdir(parents=True)
    (package_dir / "lib").mkdir()
    (package_dir / "requirements").mkdir()
    (package_dir / "licenses").mkdir()

    shutil.copy2(floor_binary, package_dir / "bin" / floor_binary.name)
    shutil.copy2(ROOT / "floor" / "launch.sh", package_dir / "launch.sh")
    shutil.copy2(
        ROOT / "lib" / "leaf-version-gate.sh",
        package_dir / "lib" / "leaf-version-gate.sh",
    )
    shutil.copy2(ROOT / "LICENSE", package_dir / "licenses" / "Leaf-Syncthing-Pak-MIT.txt")
    manifest = (ROOT / "floor" / "pak.json.in").read_text(encoding="utf-8")
    (package_dir / "pak.json").write_text(
        manifest.replace("@PAK_VERSION@", args.pak_version), encoding="utf-8"
    )
    (package_dir / "requirements" / "min-leaf-version").write_text(
        args.min_leaf_version + "\n", encoding="utf-8"
    )
    for executable in (package_dir / "launch.sh", package_dir / "bin" / floor_binary.name):
        executable.chmod(0o755)

    write_zip(package_dir, archive_path)
    installed = sum(path.stat().st_size for path in package_dir.rglob("*") if path.is_file())
    print(f"package={package_dir}")
    print(f"archive={archive_path}")
    print(f"installed_bytes={installed}")


if __name__ == "__main__":
    main()
