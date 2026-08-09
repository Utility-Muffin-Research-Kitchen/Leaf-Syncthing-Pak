#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=../lib/leaf-version-gate.sh
# shellcheck disable=SC1091
. "$ROOT_DIR/lib/leaf-version-gate.sh"

fixture="$(mktemp -d "${TMPDIR:-/tmp}/leaf-version-gate.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT
manifest="$fixture/pak.json"
release="$fixture/release.json"

expect_gate() {
    local expected=$1
    local reason=$2
    set +e
    leaf_version_gate_manifest "$manifest" "$release"
    local actual=$?
    set -e
    [ "$actual" -eq "$expected" ] || {
        echo "expected gate status $expected, got $actual" >&2
        exit 1
    }
    [ "$LEAF_VERSION_GATE_REASON" = "$reason" ] || {
        echo "expected reason $reason, got $LEAF_VERSION_GATE_REASON" >&2
        exit 1
    }
}

printf '%s\n' '{"pak_version":"1.0.0","min_leaf_version":"1.2.3"}' >"$manifest"
printf '%s\n' '{"version":"v1.2.3"}' >"$release"
expect_gate 0 compatible
[ "$LEAF_REQUIRED_VERSION" = 1.2.3 ]
[ "$LEAF_INSTALLED_VERSION" = v1.2.3 ]

printf '%s\n' '{"version":"1.2.4-beta.1"}' >"$release"
expect_gate 0 compatible
printf '%s\n' '{"version":"v1.2.2"}' >"$release"
expect_gate 67 below-minimum
printf '%s\n' '{"version":"development-build"}' >"$release"
expect_gate 66 invalid-installed-version
printf '%s\n' '{}' >"$release"
expect_gate 66 unknown-installed-version

printf '%s\n' '{"pak_version":"0.0.0-b4b"}' >"$manifest"
expect_gate 2 no-minimum
printf '%s\n' '{"min_leaf_version":"v1.2.3"}' >"$manifest"
expect_gate 65 invalid-minimum
printf '%s\n' '{"min_leaf_version":"10000.0.0"}' >"$manifest"
expect_gate 65 invalid-minimum
printf '%s\n' '{"min_leaf_version":"1.2.3","min_leaf_version":"1.2.3"}' >"$manifest"
expect_gate 65 invalid-manifest

printf '%s\n' '99.99.99' >"$fixture/minimum"
[ "$(leaf_version_read_requirement "$fixture/minimum")" = 99.99.99 ]
printf '%s\n%s\n' '99.99.99' 'unexpected' >"$fixture/minimum"
if leaf_version_read_requirement "$fixture/minimum" >/dev/null 2>&1; then
    echo "accepted a multi-line requirement" >&2
    exit 1
fi

echo "leaf version gate: PASS"
