#!/usr/bin/env python3
"""Add the locked Syncthing floor and real artifacts to a catalog candidate."""

from __future__ import annotations

import argparse
from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import zipfile


ROOT = Path(__file__).resolve().parents[1]
APP_ID = "org.umrk.syncthing"
RELEASE_BASE = (
    "https://github.com/Utility-Muffin-Research-Kitchen/"
    "Leaf-Syncthing-Pak/releases/download"
)


def fail(message: str) -> None:
    raise SystemExit(f"release catalog: {message}")


def load(path: Path) -> dict:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"cannot read {path}: {exc}")
    if not isinstance(value, dict):
        fail(f"{path} is not a JSON object")
    return value


def artifact(path: Path, version: str, minimum: str | None) -> dict:
    try:
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        with zipfile.ZipFile(path) as archive:
            manifest = json.loads(archive.read("Syncthing.pak/pak.json"))
            installed_size = sum(
                info.file_size for info in archive.infolist() if not info.is_dir()
            )
    except (OSError, KeyError, zipfile.BadZipFile, json.JSONDecodeError) as exc:
        fail(f"cannot inspect {path}: {exc}")
    if manifest.get("pak_version") != version:
        fail(f"{path} does not contain version {version}")
    runtime_minimum = manifest.get("min_leaf_version")
    if runtime_minimum != minimum:
        fail(
            f"{path} runtime minimum {runtime_minimum!r} does not match {minimum!r}"
        )
    return {
        "url": f"{RELEASE_BASE}/v{version}/Syncthing.mlp1.pak.zip",
        "name": "Syncthing.mlp1.pak.zip",
        "archive": "zip",
        "size": path.stat().st_size,
        "installed_size": installed_size,
        "sha256": digest,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, required=True)
    parser.add_argument("--real", type=Path, required=True)
    parser.add_argument("--floor", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    catalog = deepcopy(load(args.catalog))
    if catalog.get("schema") != 1 or catalog.get("product") != "pak-rat":
        fail("input is not a Pak Rat schema-1 catalog")
    apps = catalog.get("apps")
    if not isinstance(apps, list):
        fail("input catalog apps must be an array")
    if any(isinstance(app, dict) and app.get("id") == APP_ID for app in apps):
        fail("input catalog already contains Syncthing")

    lock = load(ROOT / "release-lock.json")
    metadata = load(ROOT / "pakrat.json")
    floor_version = lock["floor_version"]
    real_version = lock["pak_version"]
    minimum = lock["min_leaf_version"]
    floor_artifact = artifact(args.floor, floor_version, None)
    real_artifact = artifact(args.real, real_version, minimum)

    app = {
        key: metadata[key]
        for key in (
            "id",
            "name",
            "summary",
            "description",
            "author",
            "repo_url",
            "categories",
        )
    }
    app["version"] = floor_version
    app["packages"] = [
        {
            "platform": "mlp1",
            "runtime": "leaf",
            "version": floor_version,
            "install_name": "Syncthing.pak",
            "runtime_manifest_path": "pak.json",
            "artifact": floor_artifact,
            "versions": [
                {
                    "version": real_version,
                    "min_leaf_version": minimum,
                    "artifact": real_artifact,
                },
                {"version": floor_version, "artifact": floor_artifact},
            ],
        }
    ]
    apps.append(app)

    generated_at = datetime.now(timezone.utc).replace(microsecond=0)
    stamp = generated_at.strftime("%Y%m%dT%H%M%SZ")
    catalog["catalog_revision"] = f"candidate-syncthing-{stamp}"
    catalog["generated_at"] = generated_at.isoformat().replace("+00:00", "Z")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(catalog, indent=2) + "\n", encoding="utf-8")
    print(
        f"release catalog: wrote {args.output} with floor={floor_version} "
        f"real={real_version} leaf>={minimum}"
    )


if __name__ == "__main__":
    main()
