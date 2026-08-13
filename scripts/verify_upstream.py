#!/usr/bin/env python3
"""Verify the exact Syncthing release inputs locked for Leaf."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


class LockedRedirectHandler(HTTPRedirectHandler):
    def __init__(self, allowed_hosts: set[str]) -> None:
        super().__init__()
        self.allowed_hosts = allowed_hosts
        self.seen: list[str] = []

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        host = (urlsplit(newurl).hostname or "").lower()
        if host not in self.allowed_hosts:
            raise RuntimeError(f"redirect host is not locked: {host or '<none>'}")
        self.seen.append(host)
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def download(url: str, target: Path, expected_sha256: str,
             allowed_hosts: set[str]) -> list[str]:
    initial_host = (urlsplit(url).hostname or "").lower()
    if initial_host not in allowed_hosts:
        raise RuntimeError(f"initial download host is not locked: {initial_host}")
    redirect_handler = LockedRedirectHandler(allowed_hosts)
    opener = build_opener(redirect_handler)
    request = Request(url, headers={"User-Agent": "Leaf-Syncthing-Pak/verify-upstream"})
    temporary = target.with_name(target.name + ".tmp")
    temporary.unlink(missing_ok=True)
    with opener.open(request, timeout=60) as response, temporary.open("wb") as output:
        shutil.copyfileobj(response, output)
    actual = sha256(temporary)
    if actual != expected_sha256:
        temporary.unlink(missing_ok=True)
        raise RuntimeError(
            f"sha256 mismatch for {target.name}: {actual} != {expected_sha256}"
        )
    os.replace(temporary, target)
    return redirect_handler.seen


def run(args: list[str], **kwargs) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, check=False, text=True, encoding="utf-8",
                          errors="replace", capture_output=True, **kwargs)


def verify_tag(lock: dict) -> None:
    tag = lock["source_tag"]
    result = run([
        "git", "ls-remote", "--tags", lock["source_repository"],
        tag, f"{tag}^{{}}",
    ])
    if result.returncode != 0:
        raise RuntimeError(f"git ls-remote failed: {result.stderr.strip()}")
    refs = {}
    for line in result.stdout.splitlines():
        fields = line.split()
        if len(fields) == 2:
            refs[fields[1]] = fields[0]
    if refs.get(tag) != lock["source_tag_object"]:
        raise RuntimeError(f"tag object mismatch: {refs.get(tag)!r}")
    if refs.get(f"{tag}^{{}}") != lock["source_commit"]:
        raise RuntimeError(f"peeled source commit mismatch: {refs.get(f'{tag}^{{}}')!r}")


def verify_signatures(lock: dict, key_path: Path, checksums_path: Path,
                      source_path: Path, source_signature_path: Path) -> None:
    expected_fingerprint = lock["release_key"]["fingerprint"]
    with tempfile.TemporaryDirectory(prefix="leaf-syncthing-gnupg-") as home:
        os.chmod(home, 0o700)
        imported = run([
            "gpg", "--homedir", home, "--batch", "--import", str(key_path),
        ])
        if imported.returncode != 0:
            raise RuntimeError(f"release key import failed: {imported.stderr.strip()}")
        listed = run([
            "gpg", "--homedir", home, "--batch", "--with-colons",
            "--fingerprint", "--fingerprint",
        ])
        fingerprints = {
            line.split(":")[9]
            for line in listed.stdout.splitlines()
            if line.startswith("fpr:") and len(line.split(":")) > 9
        }
        if expected_fingerprint not in fingerprints:
            raise RuntimeError(
                f"release key fingerprint missing: {expected_fingerprint}"
            )
        verified = run([
            "gpg", "--homedir", home, "--batch", "--status-fd", "1",
            "--verify", str(checksums_path),
        ])
        valid_marker = f"[GNUPG:] VALIDSIG {expected_fingerprint} "
        if valid_marker not in verified.stdout:
            detail = (verified.stdout + "\n" + verified.stderr).strip()
            raise RuntimeError(f"expected checksum signature is not valid:\n{detail}")
        # The checksum is dual-signed. The current official key file may omit
        # the retired co-signer, making gpg return nonzero despite the pinned
        # current signature above. The fingerprint-specific VALIDSIG is the
        # acceptance condition; an arbitrary good signature is insufficient.
        source_verified = run([
            "gpg", "--homedir", home, "--batch", "--status-fd", "1",
            "--verify", str(source_signature_path), str(source_path),
        ])
        if valid_marker not in source_verified.stdout:
            detail = (source_verified.stdout + "\n" + source_verified.stderr).strip()
            raise RuntimeError(f"expected source signature is not valid:\n{detail}")


def verify_checksum_entry(lock: dict, checksums_path: Path) -> None:
    lines = checksums_path.read_text(encoding="utf-8").splitlines()
    for artifact in (lock["binary"], lock["source_offer"]):
        pattern = re.compile(rf"^([0-9a-f]{{64}})  {re.escape(artifact['name'])}$")
        matches = [
            match.group(1)
            for line in lines
            if (match := pattern.match(line))
        ]
        if matches != [artifact["sha256"]]:
            raise RuntimeError(
                f"signed checksum entry mismatch for {artifact['name']}: {matches!r}"
            )


def verify_archive(lock: dict, archive: Path) -> None:
    if archive.stat().st_size != lock["binary"]["size"]:
        raise RuntimeError(
            f"archive size mismatch: {archive.stat().st_size} != "
            f"{lock['binary']['size']}"
        )
    root = f"syncthing-linux-arm64-{lock['version']}/"
    required = {
        root + "syncthing",
        root + "LICENSE.txt",
    }
    names: set[str] = set()
    with tarfile.open(archive, "r:gz") as package:
        for member in package.getmembers():
            path = PurePosixPath(member.name)
            inside_root = member.name == root.rstrip("/") or member.name.startswith(root)
            if path.is_absolute() or ".." in path.parts or not inside_root:
                raise RuntimeError(f"unsafe or unexpected archive member: {member.name}")
            names.add(member.name)
    missing = sorted(required - names)
    if missing:
        raise RuntimeError(f"archive is missing required members: {missing}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--lock", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    lock = json.loads(args.lock.read_text(encoding="utf-8"))
    if lock.get("schema") != 1:
        raise RuntimeError(f"unsupported lock schema: {lock.get('schema')!r}")
    allowed_hosts = {host.lower() for host in lock["allowed_download_hosts"]}
    args.output.mkdir(parents=True, exist_ok=True)

    binary_path = args.output / lock["binary"]["name"]
    checksums_path = args.output / lock["checksums"]["name"]
    key_path = args.output / "release-key.txt"
    source_path = args.output / lock["source_offer"]["name"]
    source_signature_path = args.output / (lock["source_offer"]["name"] + ".asc")
    redirects = {
        "binary": download(lock["binary"]["url"], binary_path,
                           lock["binary"]["sha256"], allowed_hosts),
        "checksums": download(lock["checksums"]["url"], checksums_path,
                              lock["checksums"]["sha256"], allowed_hosts),
        "release_key": download(lock["release_key"]["url"], key_path,
                                lock["release_key"]["sha256"], allowed_hosts),
        "source": download(lock["source_offer"]["url"], source_path,
                           lock["source_offer"]["sha256"], allowed_hosts),
        "source_signature": download(
            lock["source_offer"]["signature_url"], source_signature_path,
            lock["source_offer"]["signature_sha256"], allowed_hosts,
        ),
    }
    verify_tag(lock)
    verify_signatures(lock, key_path, checksums_path,
                      source_path, source_signature_path)
    verify_checksum_entry(lock, checksums_path)
    verify_archive(lock, binary_path)

    print(json.dumps({
        "status": "verified",
        "version": lock["version"],
        "source_commit": lock["source_commit"],
        "binary": binary_path.name,
        "sha256": sha256(binary_path),
        "source_offer_sha256": sha256(source_path),
        "release_key_fingerprint": lock["release_key"]["fingerprint"],
        "redirect_hosts": redirects,
    }, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, RuntimeError, ValueError, json.JSONDecodeError) as error:
        print(f"verify-upstream: {error}", file=sys.stderr)
        raise SystemExit(1)
