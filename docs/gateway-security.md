# Leaf Syncthing gateway security

The B2 gateway is a temporary read-only browser view. The resident controller
owns its listener and binds one exact address from the current eligible physical
LAN routes. Syncthing's administrative listener remains a private Unix socket;
the gateway reaches it through a controller-owned authenticated transport.

## Two surfaces

Before trust, only `/leaf/pair` is reachable. The device displays a URL whose
256-bit random token is in the URL fragment, plus a separate four-digit PIN.
The fragment is submitted by JavaScript and is therefore not sent in the
initial HTTP request. A successful pairing consumes the offer before trust is
issued. `/leaf/logout` removes that browser's trust.

Leaf POSTs require the exact listener Host, an exact same-origin Origin or
Referer, and a per-listener CSRF token. Trust is a host-only, Secure, HttpOnly,
SameSite=Strict cookie with Path `/`. Unknown Leaf paths and all untrusted
proxy requests fail closed.

## Read-only upstream proxy

Trusted browsers may send only GET or HEAD to an exact allowlist derived from
the pinned Syncthing v2.1.2 web UI. The gateway removes client authorization,
API-key, cookie, Origin, and Referer headers before using its private upstream
transport. It rejects request bodies, method overrides, unknown query shapes,
and external or unknown redirects. Upstream cookies and CORS headers are
removed, every response is `no-store`, and `/rest/config` recursively blanks
API keys, passwords, untrusted passwords, and tokens.

The allowlist contains:

| Surface | Allowed reads |
| --- | --- |
| UI shell | `/`, the pinned scripts/styles/images/fonts, fixed templates, translations, and themes |
| Health | `/rest/noauth/health`, system status and version |
| Configuration | config and config-in-sync state, with secret redaction |
| Connectivity | connections, discovery, and display-safe system errors |
| Pending items | pending devices and folders |
| Progress | device/folder stats, database status, and completion |
| Events | bounded event and disk-event queries |
| Locale | the language-selection endpoint |

The upstream UI may still render action controls, but every mutation stops at
the gateway. Configuration changes remain available only through validated
operations in the on-device UI.

## Pairing and trust limits

- One live pairing offer; lifetime two minutes; single use.
- Five failed PIN attempts per source in 30 seconds.
- Twenty failed attempts across the device in ten minutes lock pairing until
  the user reopens it on the device.
- At most 32 trusted browsers.
- Trust expires after 30 days absolute or 24 hours idle.
- Only hashes of browser trust tokens are persisted.

## Listener lifetime

The device UI renews a four-second foreground lease while the gateway screen is
open. Leaving that screen closes the listener unless the user explicitly grants
a 15-minute extension after at least one browser has paired. Route or eligible
address changes, extension expiry, controller shutdown, profile changes, and
trust revocation close it as well.

## Certificate and local boundary

The controller creates a private self-signed certificate with the current LAN
address set in its SANs and shows its SHA-256 fingerprint on the device. A
changed address set rotates the certificate. The certificate and hashed trust
store are controller-private files protected by strict local path and
permission checks. The browser warning is expected; the displayed fingerprint
is the verification channel.

This boundary protects the network surface. It does not claim protection from
an attacker who already has root access to the handheld or can rewrite its
FAT-backed runtime files.
