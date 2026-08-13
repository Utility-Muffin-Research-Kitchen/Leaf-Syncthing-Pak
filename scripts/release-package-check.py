#!/usr/bin/env python3
"""Verify a built real or inert-floor release archive."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import stat
import zipfile


ROOT = Path(__file__).resolve().parents[1]


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(f"release package: {message}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--kind", choices=("floor", "real"), required=True)
    parser.add_argument("--archive", type=Path, required=True)
    args = parser.parse_args()

    lock = json.loads((ROOT / "release-lock.json").read_text(encoding="utf-8"))
    expected_version = lock["floor_version" if args.kind == "floor" else "pak_version"]
    digest = hashlib.sha256(args.archive.read_bytes()).hexdigest()

    with zipfile.ZipFile(args.archive) as archive:
        infos = archive.infolist()
        names = [info.filename for info in infos]
        require(len(names) == len(set(names)), "archive contains duplicate paths")
        for info in infos:
            path = PurePosixPath(info.filename)
            require(not path.is_absolute() and ".." not in path.parts,
                    f"unsafe path {info.filename!r}")
            require(path.parts and path.parts[0] == "Syncthing.pak",
                    f"path outside Syncthing.pak: {info.filename!r}")
            require(not stat.S_ISLNK(info.external_attr >> 16),
                    f"archive contains symlink {info.filename!r}")

        def read(name: str) -> bytes:
            require(name in names, f"missing {name}")
            return archive.read(name)

        manifest = json.loads(read("Syncthing.pak/pak.json"))
        require(manifest.get("id") == "org.umrk.syncthing", "package id mismatch")
        require(manifest.get("pak_version") == expected_version, "package version mismatch")

        if args.kind == "floor":
            require("min_leaf_version" not in manifest, "floor is catalog-gated")
            require("service" not in manifest, "floor contains a service manifest")
            minimum = read("Syncthing.pak/requirements/min-leaf-version").decode().strip()
            require(minimum == lock["min_leaf_version"], "floor display minimum mismatch")
            forbidden = {
                "Syncthing.pak/service.sh",
                "Syncthing.pak/bin/syncthing",
                "Syncthing.pak/bin/leaf-syncthing",
                "Syncthing.pak/bin/leaf-syncthing-ui",
            }
            require(not forbidden.intersection(names), "floor contains real-package files")
        else:
            require(manifest.get("min_leaf_version") == lock["min_leaf_version"],
                    "runtime minimum mismatch")
            require(isinstance(manifest.get("service"), dict), "real package has no service")
            for name in (
                "Syncthing.pak/service.sh",
                "Syncthing.pak/bin/syncthing",
                "Syncthing.pak/bin/leaf-syncthing",
                "Syncthing.pak/bin/leaf-syncthing-ui",
                "Syncthing.pak/licenses/Leaf-Syncthing-Pak-MIT.txt",
                "Syncthing.pak/licenses/Syncthing-MPL-2.0.txt",
            ):
                read(name)
            upstream_name = PurePosixPath(lock["upstream_lock"]).name
            packaged_lock = json.loads(read(f"Syncthing.pak/licenses/{upstream_name}"))
            source_lock = json.loads((ROOT / lock["upstream_lock"]).read_text(encoding="utf-8"))
            require(packaged_lock == source_lock, "packaged upstream lock mismatch")

        installed_size = sum(info.file_size for info in infos if not info.is_dir())

    print(json.dumps({
        "archive": str(args.archive),
        "kind": args.kind,
        "version": expected_version,
        "min_leaf_version": lock["min_leaf_version"],
        "size": args.archive.stat().st_size,
        "installed_size": installed_size,
        "sha256": digest,
    }, sort_keys=True))


if __name__ == "__main__":
    main()
