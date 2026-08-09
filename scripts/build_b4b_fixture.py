#!/usr/bin/env python3
"""Build a disposable floor+gated B4b feed without touching production data."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
WORKSPACE = ROOT.parent
VERSION_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+$")


def version(value: str) -> str:
    if not VERSION_RE.fullmatch(value) or any(
        int(component) > 9999 for component in value.split(".")
    ):
        raise argparse.ArgumentTypeError("must be an exact MAJOR.MINOR.PATCH")
    return value


def run(command: list[str], cwd: Path) -> None:
    print(f"+ (cd {cwd} && {' '.join(command)})")
    subprocess.run(command, cwd=cwd, check=True)


def write_metadata(app_dir: Path, package_version: str, minimum: str | None) -> None:
    template = (ROOT / "pakrat.json.in").read_text(encoding="utf-8")
    rendered = template.replace("@PAK_VERSION@", package_version).replace(
        "@MIN_LEAF_VERSION@", minimum or "0.0.0"
    )
    metadata = json.loads(rendered)
    package = metadata["leaf"]["packages"][0]
    if minimum is None:
        package.pop("min_leaf_version")
    (app_dir / "pakrat.json").write_text(
        json.dumps(metadata, indent=2) + "\n", encoding="utf-8"
    )


def generate(
    leaf_dir: Path,
    output: Path,
    app_dir: Path,
    base_url: str,
    history: Path | None = None,
) -> Path:
    shutil.rmtree(output, ignore_errors=True)
    command = [
        sys.executable,
        str(leaf_dir / "scripts" / "pakrat-local-feed.py"),
        "--output",
        str(output),
        "--base-url",
        base_url,
        "--app-dir",
        str(app_dir),
        "--skip-build",
    ]
    if history is not None:
        command += ["--history", str(history)]
    run(command, leaf_dir)
    return output / "pakrat" / "v1" / "storefront.json"


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--floor-version", type=version, default="0.0.1")
    parser.add_argument("--real-version", type=version, default="0.0.2")
    parser.add_argument("--min-leaf-version", type=version, default="99.99.99")
    parser.add_argument(
        "--base-url", default="http://127.0.0.1:8765/pakrat/v1/"
    )
    parser.add_argument("--skip-build", action="store_true")
    args = parser.parse_args()

    if int(args.min_leaf_version.split(".")[0]) < 90:
        parser.error("B4b fixtures require a conspicuously disposable minimum >= 90.0.0")
    if tuple(map(int, args.real_version.split("."))) <= tuple(
        map(int, args.floor_version.split("."))
    ):
        parser.error("real version must be newer than the floor")

    leaf_dir = WORKSPACE / "Leaf"
    leaf_docs_dir = WORKSPACE / "leaf-docs"
    if not (leaf_dir / "scripts" / "pakrat-local-feed.py").is_file():
        raise SystemExit(f"missing Leaf local-feed generator: {leaf_dir}")
    production_catalog = leaf_docs_dir / "public" / "pakrat" / "v1" / "storefront.json"
    if not production_catalog.is_file():
        raise SystemExit(f"missing production catalog baseline: {production_catalog}")

    if not args.skip_build:
        run(
            [
                "make",
                "package-floor-mlp1",
                f"FLOOR_PAK_VERSION={args.floor_version}",
                f"MIN_LEAF_VERSION={args.min_leaf_version}",
            ],
            ROOT,
        )
        run(
            [
                "make",
                "package-mlp1",
                f"PAK_VERSION={args.real_version}",
                f"MIN_LEAF_VERSION={args.min_leaf_version}",
            ],
            ROOT,
        )

    floor_package = ROOT / "build" / "mlp1" / "floor" / "package" / "Syncthing.pak"
    real_package = ROOT / "build" / "mlp1" / "package" / "Syncthing.pak"
    if not floor_package.is_dir() or not real_package.is_dir():
        raise SystemExit("missing floor or real package; rerun without --skip-build")

    fixture_root = ROOT / "build" / "b4b-fixture"
    app_dir = fixture_root / "app"
    package_dir = app_dir / "build" / "mlp1" / "package" / "Syncthing.pak"
    shutil.rmtree(fixture_root, ignore_errors=True)
    package_dir.parent.mkdir(parents=True)

    shutil.copytree(floor_package, package_dir)
    write_metadata(app_dir, args.floor_version, None)
    floor_output = leaf_dir / "build" / "pakrat-local" / "syncthing-b4b-floor"
    floor_catalog = generate(
        leaf_dir, floor_output, app_dir, args.base_url
    )

    shutil.rmtree(package_dir)
    shutil.copytree(real_package, package_dir)
    write_metadata(app_dir, args.real_version, args.min_leaf_version)
    final_output = leaf_dir / "build" / "pakrat-local" / "syncthing-b4b"
    storefront_path = generate(
        leaf_dir, final_output, app_dir, args.base_url, floor_catalog
    )

    generated = json.loads(storefront_path.read_text(encoding="utf-8"))
    baseline = json.loads(production_catalog.read_text(encoding="utf-8"))
    generated_apps = [
        app for app in generated["apps"] if app.get("id") == "org.umrk.syncthing"
    ]
    if len(generated_apps) != 1:
        raise SystemExit("generated feed did not contain exactly one Syncthing app")
    if any(app.get("id") == "org.umrk.syncthing" for app in baseline["apps"]):
        raise SystemExit("production baseline already contains Syncthing")

    syncthing = generated_apps[0]
    package = syncthing["packages"][0]
    versions = package["versions"]
    if [value["version"] for value in versions] != [
        args.real_version,
        args.floor_version,
    ]:
        raise SystemExit("generated versions are not strictly real-then-floor")
    if versions[0].get("min_leaf_version") != args.min_leaf_version:
        raise SystemExit("real version does not carry the disposable minimum")
    if "min_leaf_version" in versions[1]:
        raise SystemExit("floor version is unexpectedly gated")
    if syncthing["version"] != args.floor_version or package["version"] != args.floor_version:
        raise SystemExit("legacy fields are not pinned to the floor")

    combined = dict(baseline)
    combined["catalog_revision"] = generated["catalog_revision"] + "-combined"
    combined["generated_at"] = generated["generated_at"]
    combined["apps"] = [*baseline["apps"], syncthing]
    storefront_path.write_text(
        json.dumps(combined, indent=2) + "\n", encoding="utf-8"
    )

    baseline_ids = [app["id"] for app in baseline["apps"]]
    combined_ids = [app["id"] for app in combined["apps"]]
    if combined_ids[:-1] != baseline_ids:
        raise SystemExit("combining Syncthing changed another storefront app")

    print(f"B4b feed: {storefront_path}")
    print(f"Other apps preserved: {len(baseline_ids)}")
    print(
        f"Syncthing history: gated {args.real_version} requires "
        f"{args.min_leaf_version}; floor {args.floor_version} is ungated"
    )


if __name__ == "__main__":
    main()
