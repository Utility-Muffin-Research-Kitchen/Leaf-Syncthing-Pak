#!/usr/bin/env python3
"""Assemble the non-production B1 MLP1 controller package."""

from __future__ import annotations

import json
from pathlib import Path, PurePosixPath
import shutil
import stat
import tarfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]
LOCK_PATH = ROOT / "upstream" / "syncthing-v2.1.2.lock.json"
UPSTREAM_DIR = ROOT / "workdir" / "upstream" / "v2.1.2"
PACKAGE_DIR = ROOT / "build" / "mlp1" / "package" / "Syncthing.pak"
ARCHIVE_PATH = ROOT / "build" / "mlp1" / "Syncthing.mlp1.pak.zip"


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
    lock = json.loads(LOCK_PATH.read_text(encoding="utf-8"))
    version = lock["version"]
    binary_archive = UPSTREAM_DIR / lock["binary"]["name"]
    controller = ROOT / "build" / "mlp1" / "bin" / "leaf-syncthing"
    if not binary_archive.is_file() or not controller.is_file():
        raise SystemExit("verified upstream archive or controller binary is missing")

    if PACKAGE_DIR.parent.exists():
        shutil.rmtree(PACKAGE_DIR.parent)
    PACKAGE_DIR.mkdir(parents=True)
    archive_root = f"syncthing-linux-arm64-{version}"
    with tarfile.open(binary_archive, "r:gz") as package:
        copy_archive_member(package, f"{archive_root}/syncthing", PACKAGE_DIR / "bin" / "syncthing")
        copy_archive_member(package, f"{archive_root}/LICENSE.txt", PACKAGE_DIR / "licenses" / "Syncthing-MPL-2.0.txt")

    shutil.copy2(controller, PACKAGE_DIR / "bin" / "leaf-syncthing")
    shutil.copy2(ROOT / "launch.sh", PACKAGE_DIR / "launch.sh")
    shutil.copy2(ROOT / "pak.json", PACKAGE_DIR / "pak.json")
    shutil.copy2(LOCK_PATH, PACKAGE_DIR / "licenses" / LOCK_PATH.name)
    for executable in (PACKAGE_DIR / "launch.sh", PACKAGE_DIR / "bin" / "syncthing", PACKAGE_DIR / "bin" / "leaf-syncthing"):
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
