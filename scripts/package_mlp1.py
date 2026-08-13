#!/usr/bin/env python3
"""Assemble the MLP1 controller and UI package."""

from __future__ import annotations

import argparse
import json
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import tarfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]
LOCK_PATH = ROOT / "upstream" / "syncthing-v2.1.3.lock.json"
UPSTREAM_DIR = ROOT / "workdir" / "upstream" / "v2.1.3"
PACKAGE_DIR = ROOT / "build" / "mlp1" / "package" / "Syncthing.pak"
ARCHIVE_PATH = ROOT / "build" / "mlp1" / "Syncthing.mlp1.pak.zip"
VERSION_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+$")


def package_version(value: str) -> str:
    if not VERSION_RE.fullmatch(value) or any(
        int(component) > 9999 for component in value.split(".")
    ):
        raise argparse.ArgumentTypeError("must be an exact MAJOR.MINOR.PATCH")
    return value


def copy_archive_member(package: tarfile.TarFile, name: str, target: Path) -> None:
    path = PurePosixPath(name)
    if path.is_absolute() or ".." in path.parts:
        raise RuntimeError(f"unsafe archive path: {name}")
    member = package.getmember(name)
    source = package.extractfile(member)
    if source is None or not member.isfile():
        raise RuntimeError(f"archive member is not a file: {name}")
    target.parent.mkdir(parents=True, exist_ok=True)
    with source, target.open("wb") as output:
        shutil.copyfileobj(source, output)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--pak-version", type=package_version)
    parser.add_argument("--min-leaf-version", type=package_version)
    args = parser.parse_args()
    if (args.pak_version is None) != (args.min_leaf_version is None):
        parser.error("--pak-version and --min-leaf-version must be supplied together")

    lock = json.loads(LOCK_PATH.read_text(encoding="utf-8"))
    version = lock["version"]
    binary_archive = UPSTREAM_DIR / lock["binary"]["name"]
    controller = ROOT / "build" / "mlp1" / "bin" / "leaf-syncthing"
    ui = ROOT / "build" / "mlp1" / "bin" / "leaf-syncthing-ui"
    floor = ROOT / "build" / "mlp1" / "bin" / "leaf-syncthing-floor"
    if not all(path.is_file() for path in (binary_archive, controller, ui, floor)):
        raise SystemExit("verified upstream, controller, or device UI binary is missing")

    if PACKAGE_DIR.parent.exists():
        shutil.rmtree(PACKAGE_DIR.parent)
    PACKAGE_DIR.mkdir(parents=True)
    archive_root = f"syncthing-linux-arm64-{version}"
    with tarfile.open(binary_archive, "r:gz") as package:
        copy_archive_member(package, f"{archive_root}/syncthing", PACKAGE_DIR / "bin" / "syncthing")
        copy_archive_member(package, f"{archive_root}/LICENSE.txt", PACKAGE_DIR / "licenses" / "Syncthing-MPL-2.0.txt")

    shutil.copy2(controller, PACKAGE_DIR / "bin" / "leaf-syncthing")
    shutil.copy2(ui, PACKAGE_DIR / "bin" / "leaf-syncthing-ui")
    shutil.copy2(floor, PACKAGE_DIR / "bin" / "leaf-syncthing-floor")
    shutil.copy2(ROOT / "launch.sh", PACKAGE_DIR / "launch.sh")
    shutil.copy2(ROOT / "service.sh", PACKAGE_DIR / "service.sh")
    (PACKAGE_DIR / "lib").mkdir()
    shutil.copy2(
        ROOT / "lib" / "leaf-version-gate.sh",
        PACKAGE_DIR / "lib" / "leaf-version-gate.sh",
    )
    manifest = json.loads((ROOT / "pak.json").read_text(encoding="utf-8"))
    if args.pak_version is not None:
        manifest["name"] = "Syncthing"
        manifest["description"] = "Optional managed Syncthing service for Leaf."
        manifest["pak_version"] = args.pak_version
        manifest["min_leaf_version"] = args.min_leaf_version
    (PACKAGE_DIR / "pak.json").write_text(
        json.dumps(manifest, indent=2) + "\n", encoding="utf-8"
    )
    shutil.copy2(ROOT / "LICENSE", PACKAGE_DIR / "licenses" / "Leaf-Syncthing-Pak-MIT.txt")
    shutil.copy2(ROOT / "third_party" / "qrcodegen.LICENSE", PACKAGE_DIR / "licenses" / "qrcodegen-MIT.txt")
    shutil.copy2(LOCK_PATH, PACKAGE_DIR / "licenses" / LOCK_PATH.name)
    for executable in (
        PACKAGE_DIR / "launch.sh",
        PACKAGE_DIR / "service.sh",
        PACKAGE_DIR / "bin" / "syncthing",
        PACKAGE_DIR / "bin" / "leaf-syncthing",
        PACKAGE_DIR / "bin" / "leaf-syncthing-ui",
        PACKAGE_DIR / "bin" / "leaf-syncthing-floor",
    ):
        executable.chmod(0o755)

    if ARCHIVE_PATH.exists():
        ARCHIVE_PATH.unlink()
    with zipfile.ZipFile(ARCHIVE_PATH, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as output:
        for path in sorted(PACKAGE_DIR.rglob("*")):
            if not path.is_file():
                continue
            relative = PurePosixPath(PACKAGE_DIR.name) / path.relative_to(PACKAGE_DIR)
            mode = 0o755 if path.stat().st_mode & stat.S_IXUSR else 0o644
            info = zipfile.ZipInfo(str(relative), date_time=(1980, 1, 1, 0, 0, 0))
            info.external_attr = (stat.S_IFREG | mode) << 16
            info.create_system = 3
            output.writestr(info, path.read_bytes(), compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)

    installed = sum(path.stat().st_size for path in PACKAGE_DIR.rglob("*") if path.is_file())
    print(f"package={PACKAGE_DIR}")
    print(f"archive={ARCHIVE_PATH}")
    print(f"installed_bytes={installed}")


if __name__ == "__main__":
    main()
