#!/bin/sh

# Shared runtime gate for the real and inert-floor Syncthing paks. Callers set
# SDCARD_PATH and PLATFORM before invoking leaf_version_gate_manifest.

leaf_version_json_string() {
    _lvg_file=$1
    _lvg_key=$2
    [ -f "$_lvg_file" ] || return 1
    _lvg_count=$(awk -v needle="\"$_lvg_key\"" '
        {
            line = $0
            while ((position = index(line, needle)) != 0) {
                count++
                line = substr(line, position + length(needle))
            }
        }
        END { print count + 0 }
    ' "$_lvg_file") || return 2
    [ "$_lvg_count" -eq 1 ] || {
        [ "$_lvg_count" -eq 0 ] && return 1
        return 2
    }
    _lvg_values=$(sed -n \
        "s/^.*\"$_lvg_key\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" \
        "$_lvg_file") || return 2
    [ -n "$_lvg_values" ] || return 2
    printf '%s\n' "$_lvg_values"
}

leaf_version_strip_zeros() {
    _lvg_number=$1
    while [ "${#_lvg_number}" -gt 1 ] && [ "${_lvg_number#0}" != "$_lvg_number" ]; do
        _lvg_number=${_lvg_number#0}
    done
    printf '%s\n' "$_lvg_number"
}

leaf_version_components() {
    _lvg_value=$1
    _lvg_allow_release=$2
    if [ "$_lvg_allow_release" -eq 1 ]; then
        case "$_lvg_value" in
            v*|V*) _lvg_value=${_lvg_value#?} ;;
        esac
    fi

    _lvg_major=${_lvg_value%%.*}
    _lvg_rest=${_lvg_value#*.}
    [ "$_lvg_rest" != "$_lvg_value" ] || return 1
    _lvg_minor=${_lvg_rest%%.*}
    _lvg_patch_and_suffix=${_lvg_rest#*.}
    [ "$_lvg_patch_and_suffix" != "$_lvg_rest" ] || return 1
    _lvg_patch=${_lvg_patch_and_suffix%%[!0-9]*}
    _lvg_suffix=${_lvg_patch_and_suffix#"$_lvg_patch"}

    case "$_lvg_major:$_lvg_minor:$_lvg_patch" in
        *[!0-9:]*|::*|*::|:*) return 1 ;;
    esac
    if [ "$_lvg_allow_release" -eq 0 ] && [ -n "$_lvg_suffix" ]; then
        return 1
    fi
    case "$_lvg_suffix" in
        .*) return 1 ;;
    esac

    _lvg_major=$(leaf_version_strip_zeros "$_lvg_major")
    _lvg_minor=$(leaf_version_strip_zeros "$_lvg_minor")
    _lvg_patch=$(leaf_version_strip_zeros "$_lvg_patch")
    [ "$_lvg_major" -le 9999 ] && [ "$_lvg_minor" -le 9999 ] &&
        [ "$_lvg_patch" -le 9999 ] || return 1
    printf '%s %s %s\n' "$_lvg_major" "$_lvg_minor" "$_lvg_patch"
}

leaf_version_at_least() {
    _lvg_installed=$(leaf_version_components "$1" 1) || return 2
    _lvg_required=$(leaf_version_components "$2" 0) || return 3
    # Intentional split of the validated three-component helper output.
    # shellcheck disable=SC2086
    set -- $_lvg_installed
    _lvg_installed_major=$1
    _lvg_installed_minor=$2
    _lvg_installed_patch=$3
    # shellcheck disable=SC2086
    set -- $_lvg_required
    _lvg_required_major=$1
    _lvg_required_minor=$2
    _lvg_required_patch=$3

    [ "$_lvg_installed_major" -gt "$_lvg_required_major" ] && return 0
    [ "$_lvg_installed_major" -lt "$_lvg_required_major" ] && return 1
    [ "$_lvg_installed_minor" -gt "$_lvg_required_minor" ] && return 0
    [ "$_lvg_installed_minor" -lt "$_lvg_required_minor" ] && return 1
    [ "$_lvg_installed_patch" -ge "$_lvg_required_patch" ]
}

leaf_version_gate_manifest() {
    _lvg_manifest=$1
    _lvg_release_json=$2
    LEAF_REQUIRED_VERSION=
    LEAF_INSTALLED_VERSION=
    LEAF_VERSION_GATE_REASON=

    LEAF_REQUIRED_VERSION=$(leaf_version_json_string "$_lvg_manifest" min_leaf_version)
    _lvg_status=$?
    case "$_lvg_status" in
        0) ;;
        1) LEAF_VERSION_GATE_REASON=no-minimum; return 2 ;;
        *) LEAF_VERSION_GATE_REASON=invalid-manifest; return 65 ;;
    esac
    leaf_version_components "$LEAF_REQUIRED_VERSION" 0 >/dev/null 2>&1 || {
        LEAF_VERSION_GATE_REASON=invalid-minimum
        return 65
    }

    LEAF_INSTALLED_VERSION=$(leaf_version_json_string "$_lvg_release_json" version)
    _lvg_status=$?
    [ "$_lvg_status" -eq 0 ] || {
        LEAF_INSTALLED_VERSION=Unknown
        LEAF_VERSION_GATE_REASON=unknown-installed-version
        return 66
    }
    leaf_version_components "$LEAF_INSTALLED_VERSION" 1 >/dev/null 2>&1 || {
        LEAF_VERSION_GATE_REASON=invalid-installed-version
        return 66
    }
    if leaf_version_at_least "$LEAF_INSTALLED_VERSION" "$LEAF_REQUIRED_VERSION"; then
        LEAF_VERSION_GATE_REASON=compatible
        return 0
    fi
    # Returned to the sourcing launcher/service wrapper.
    # shellcheck disable=SC2034
    LEAF_VERSION_GATE_REASON=below-minimum
    return 67
}

leaf_version_read_requirement() {
    _lvg_requirement_file=$1
    [ -f "$_lvg_requirement_file" ] || return 1
    _lvg_requirement=$(sed -n '1p' "$_lvg_requirement_file") || return 1
    [ "$(wc -l <"$_lvg_requirement_file" | tr -d ' ')" -eq 1 ] || return 1
    leaf_version_components "$_lvg_requirement" 0 >/dev/null 2>&1 || return 1
    printf '%s\n' "$_lvg_requirement"
}
