#!/usr/bin/env bash
set -euo pipefail

serial="${ADB_SERIAL:-}"
if [[ -z "$serial" ]]; then
    serial="$(adb devices | awk 'NR > 1 && $2 == "device" { print $1; exit }')"
fi
[[ -n "$serial" ]] || { echo "No online adb device found." >&2; exit 1; }

ADB=(adb -s "$serial")
REMOTE_CTL="${REMOTE_CTL:-/media/sdcard1/.system/leaf/platforms/mlp1/launcher/bin/jawaka-platformctl}"
REMOTE_CONTROL_SOCKET="${REMOTE_CONTROL_SOCKET:-/tmp/jawaka-runtime/services/org.umrk.syncthing/control.sock}"
tmp_dir="$(mktemp -d)"
stage="initialize"

request() {
    "${ADB[@]}" shell "'$REMOTE_CTL' --socket '$REMOTE_CONTROL_SOCKET' request '$1'" | tr -d '\r'
}

cleanup() {
    result=$?
    request '{"v":1,"id":"gateway-close-cleanup","op":"gateway.close","args":{}}' >/dev/null 2>&1 || true
    rm -r -- "$tmp_dir"
    if [[ $result -ne 0 ]]; then
        echo "FAIL real-device gateway smoke at: $stage" >&2
    fi
    return "$result"
}
trap cleanup EXIT

stage="open listener"
gateway_json="$(request '{"v":1,"id":"gateway-real-open","op":"gateway.open","args":{}}')"
url="$(printf '%s' "$gateway_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["gateway"]["url"])')"
qr_url="$(printf '%s' "$gateway_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["gateway"]["qr_url"])')"
token="$(printf '%s' "$qr_url" | python3 -c 'import sys,urllib.parse; print(urllib.parse.parse_qs(urllib.parse.urlparse(sys.stdin.read()).fragment)["token"][0])')"
origin="${url%/}"

stage="load pairing bootstrap"
curl -ksS --connect-timeout 3 --max-time 5 "${url}leaf/pair" -o "$tmp_dir/pair.html"
csrf="$(python3 -c 'import re,sys; data=open(sys.argv[1], encoding="utf-8").read(); match=re.search(r"const csrf=\"([^\"]+)\"", data); assert match; print(match.group(1))' "$tmp_dir/pair.html")"

stage="submit QR token"
printf '{"token":"%s"}' "$token" >"$tmp_dir/pair.json"
if ! curl -ksS --connect-timeout 3 --max-time 5 \
    -D "$tmp_dir/pair.headers" -o "$tmp_dir/pair.body" -w '%{http_code}' \
    -c "$tmp_dir/cookies" -H 'Content-Type: application/json' \
    -H "Origin: $origin" -H "X-Leaf-CSRF: $csrf" \
    --data-binary @"$tmp_dir/pair.json" "${url}leaf/pair" >"$tmp_dir/pair.code"; then
    echo "Pairing transport failed." >&2
    exit 1
fi
pair_code="$(<"$tmp_dir/pair.code")"
if [[ "$pair_code" != 204 ]]; then
    echo "Pairing returned HTTP $pair_code: $(tr '\n' ' ' <"$tmp_dir/pair.body")" >&2
    exit 1
fi
stage="verify trust cookie"
cookie_header="$(tr -d '\r' <"$tmp_dir/pair.headers" | grep -Ei '^Set-Cookie: ' | head -1)"
[[ "$cookie_header" == *'; Path=/'* ]]
[[ "$cookie_header" == *'; HttpOnly'* ]]
[[ "$cookie_header" == *'; Secure'* ]]
[[ "$cookie_header" == *'; SameSite=Strict'* ]]
[[ "$cookie_header" != *'; Domain='* ]]

stage="load pinned UI"
curl -ksS --connect-timeout 3 --max-time 5 -b "$tmp_dir/cookies" \
    -D "$tmp_dir/root.headers" "$url" -o "$tmp_dir/root.html"
grep -Fi 'syncthing' "$tmp_dir/root.html" >/dev/null
tr -d '\r' <"$tmp_dir/root.headers" | grep -Fi 'Cache-Control: no-store' >/dev/null

stage="read status and long-poll"
curl -ksS --connect-timeout 3 --max-time 5 -b "$tmp_dir/cookies" \
    "${url}rest/system/status" -o "$tmp_dir/status.json"
python3 -c 'import json,sys; assert isinstance(json.load(open(sys.argv[1])), dict)' "$tmp_dir/status.json"
curl -ksS --connect-timeout 3 --max-time 5 -b "$tmp_dir/cookies" \
    "${url}rest/events?since=0&limit=1&timeout=1" -o "$tmp_dir/events.json"
python3 -c 'import json,sys; assert isinstance(json.load(open(sys.argv[1])), list)' "$tmp_dir/events.json"

stage="verify redacted configuration view"
curl -ksS --connect-timeout 3 --max-time 5 -b "$tmp_dir/cookies" \
    "${url}rest/config" -o "$tmp_dir/config.json"
python3 - "$tmp_dir/config.json" <<'PY'
import json
import sys

def assert_redacted(value):
    if isinstance(value, dict):
        for name, child in value.items():
            if name.lower() in {"apikey", "password", "untrustedpassword", "token"}:
                assert child == "", f"sensitive field {name} was not redacted"
            else:
                assert_redacted(child)
    elif isinstance(value, list):
        for child in value:
            assert_redacted(child)

with open(sys.argv[1], encoding="utf-8") as source:
    assert_redacted(json.load(source))
PY

stage="deny unknown and mutating routes"
unknown="$(curl -ksS --connect-timeout 3 --max-time 5 -b "$tmp_dir/cookies" \
    -o /dev/null -w '%{http_code}' "${url}rest/system/shutdown")"
[[ "$unknown" == 404 ]]
mutation="$(curl -ksS --connect-timeout 3 --max-time 5 -b "$tmp_dir/cookies" \
    -X POST -o /dev/null -w '%{http_code}' "${url}rest/config")"
[[ "$mutation" == 405 ]]

stage="verify certificate fingerprint"
reported="$(printf '%s' "$gateway_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["gateway"]["fingerprint"])')"
hostport="${origin#https://}"
observed="$(printf '' | openssl s_client -connect "$hostport" 2>/dev/null |
    openssl x509 -noout -fingerprint -sha256 |
    sed -e 's/^sha256 Fingerprint=//' -e 's/^SHA256 Fingerprint=//')"
[[ "$reported" == "$observed" ]]

stage="logout and revoke browser"
printf '{}' |
    curl -ksS --connect-timeout 3 --max-time 5 -b "$tmp_dir/cookies" \
        -H 'Content-Type: application/json' -H "Origin: $origin" \
        -H "X-Leaf-CSRF: $csrf" --data-binary @- -o /dev/null \
        -w '%{http_code}' "${url}leaf/logout" >"$tmp_dir/logout.code"
logout_code="$(<"$tmp_dir/logout.code")"
[[ "$logout_code" == 204 ]]
after="$(curl -ksS --connect-timeout 3 --max-time 5 -b "$tmp_dir/cookies" \
    -o /dev/null -w '%{http_code}' "$url")"
[[ "$after" == 401 ]]

stage="close listener"
request '{"v":1,"id":"gateway-real-close","op":"gateway.close","args":{}}' >/dev/null
if curl -ksS --connect-timeout 1 --max-time 2 "$url" -o /dev/null 2>/dev/null; then
    echo "Gateway listener remained reachable after close." >&2
    exit 1
fi

echo "PASS real-device gateway QR pairing, cookie policy, UI, long-poll, config redaction, read-only denial, fingerprint, logout, and close"
