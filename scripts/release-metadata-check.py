#!/usr/bin/env python3
"""Reject drift between the Syncthing release lock and package metadata."""

from __future__ import annotations

import json
from pathlib import Path
import re


ROOT = Path(__file__).resolve().parents[1]
VERSION_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+$")


def load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(f"release metadata: {message}")


def main() -> None:
    lock = load(ROOT / "release-lock.json")
    require(lock.get("schema") == 1, "unsupported release-lock schema")
    for field in ("pak_version", "floor_version", "min_leaf_version"):
        value = lock.get(field, "")
        require(isinstance(value, str) and VERSION_RE.fullmatch(value) is not None,
                f"{field} must be MAJOR.MINOR.PATCH")
    require(tuple(map(int, lock["floor_version"].split("."))) <
            tuple(map(int, lock["pak_version"].split("."))),
            "floor_version must be older than pak_version")
    require(lock.get("go_version") == "1.22.12", "unexpected Go release toolchain")

    upstream_path = ROOT / lock.get("upstream_lock", "")
    require(upstream_path.parent == ROOT / "upstream" and upstream_path.is_file(),
            "upstream_lock must name a checked-in upstream lock")
    upstream = load(upstream_path)
    require(upstream.get("schema") == 1, "unsupported upstream lock schema")
    require(upstream.get("version", "").startswith("v"),
            "upstream version must be a release tag")

    manifest = load(ROOT / "pak.json")
    require(manifest.get("id") == "org.umrk.syncthing", "runtime id mismatch")
    require(manifest.get("name") == "Syncthing", "runtime name is not production")
    require(manifest.get("pak_version") == lock["pak_version"],
            "runtime pak_version does not match release-lock")
    require(manifest.get("min_leaf_version") == lock["min_leaf_version"],
            "runtime min_leaf_version does not match release-lock")
    require(isinstance(manifest.get("service"), dict), "real runtime has no service")

    catalog = load(ROOT / "pakrat.json")
    require(catalog.get("id") == manifest["id"], "Pak Rat id mismatch")
    packages = catalog.get("leaf", {}).get("packages", [])
    require(isinstance(packages, list) and len(packages) == 1,
            "Pak Rat metadata must have exactly one package")
    package = packages[0]
    require(package.get("platform") == "mlp1", "Pak Rat platform mismatch")
    require(package.get("version") == lock["pak_version"],
            "Pak Rat version does not match release-lock")
    require(package.get("min_leaf_version") == lock["min_leaf_version"],
            "Pak Rat minimum does not match release-lock")
    require(package.get("artifact_name") == "Syncthing.mlp1.pak.zip",
            "unexpected Pak Rat artifact name")
    require(package.get("install_name") == "Syncthing.pak",
            "unexpected Pak Rat install name")
    require(package.get("build_command") ==
            ["make", "package-platform", "PLATFORM=mlp1"],
            "Pak Rat build command mismatch")

    floor_template = load(ROOT / "floor" / "pak.json.in")
    require(floor_template.get("pak_version") == "@PAK_VERSION@",
            "floor manifest lost its version placeholder")
    require("min_leaf_version" not in floor_template and "service" not in floor_template,
            "floor manifest must remain ungated and inert")

    for version in (lock["floor_version"], lock["pak_version"]):
        require((ROOT / "docs" / f"release-notes-v{version}.md").is_file(),
                f"missing release notes for v{version}")

    print(
        "release metadata: "
        f"floor={lock['floor_version']} real={lock['pak_version']} "
        f"leaf>={lock['min_leaf_version']} upstream={upstream['version']}"
    )


if __name__ == "__main__":
    main()
