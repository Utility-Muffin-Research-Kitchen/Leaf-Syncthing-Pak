# UI control protocol v1

This is the package-private protocol between the resident
`leaf-syncthing service run` controller and the foreground C/Catastrophe
`leaf-syncthing-ui`. It is not a Leaf-wide capability and is not served on a
network interface.

## Transport and socket

- Path:
  `$UMRK_RUNTIME_PATH/services/org.umrk.syncthing/control.sock`.
- The controller creates a Unix stream socket with mode `0600`, distinct from
  `syncthing-gui.sock`, and removes it on exit.
- Each connection carries exactly one request and one response, then closes.
- Each message is UTF-8 JSON prefixed by a four-byte unsigned big-endian byte
  length. The semantic payload ceiling is 64 KiB. Short reads and writes must
  be retried; a partial or oversized frame is rejected.
- The socket is available only after the pinned upstream is ready. A missing
  socket means the controller is unavailable; the UI uses CTL-1 for generic
  service status and recovery actions.

## Envelope

Every request contains exactly these fields:

```json
{"v":1,"id":"request-id","op":"status.get","args":{}}
```

`v` is the integer `1`. `id` is 1–64 ASCII letters, digits, `.`, `_`, `:`, or
`-`. `op` selects an operation and `args` is its object. Unknown request fields,
missing fields, and wrong types fail closed.

A successful response is:

```json
{"v":1,"id":"request-id","ok":true,"result":{}}
```

An error response is:

```json
{"v":1,"id":"request-id","ok":false,"error":{"code":"unsupported-op","message":"unsupported UI control operation"}}
```

The frozen envelope errors are `bad-request`, `unsupported-version`,
`unsupported-op`, `bad-arguments`, and `internal`. Operations may additionally
return `not-found`, `card-absent`, `card-read-only`, `invalid-card-id`, or
`operation-failed`. Messages are display-safe and never contain API keys,
certificates, tokens, config contents, or peer secrets. An unusable request id
is returned as an empty string.

Request objects are strict. Response result objects are append-only: a v1
client must ignore unknown result members and use `capabilities` before showing
an action. Adding an operation or optional result member is compatible;
removing a member, renaming one, changing its type/meaning, or changing framing
requires v2.

## `status.get`

`args` must be `{}`. The current B1 controller advertises `status.get` and
`card.enroll`, and returns:

```text
controller                         running | recovery-pending | error
upstream.state                     stopped | starting | running | error | conflict
upstream.version                   pinned Syncthing version
upstream.device_id                 certificate-derived device id
game.active/launch_id/source_id    reconciled LIFE-1 state
recovery.state/changed             ready | pending | error; whether startup recovery changed config
network.profile/allowed_networks   lan-only | sync-anywhere; current route-derived CIDRs
cards[]                            enrolled/configured physical-card rows
folders[]                          managed-folder rows
issues[]                           display-safe controller/card/folder issues
capabilities[]                     supported operation names
```

Card rows freeze physical identity, current slot/root, state (`absent`,
`unenrolled`, `enrolled`, `invalid`, or `duplicate`), presence, writability,
duplicate-id state, retained bytes, and scoped issues. Folder rows freeze
identity, card/kind/path/type, pause state and reasons, sizes, peers, last sync,
versioning, and scoped issues. Counts and byte sizes are non-negative; an empty
timestamp is unknown. B1 normally returns no folder rows because B3 owns
onboarding; if a strict Leaf binding already exists, B1 forces it paused before
spawn and returns its reconciled safety state and issues.

## `card.enroll`

```json
{"v":1,"id":"enroll-primary","op":"card.enroll","args":{"source_id":"primary"}}
```

`source_id` names one configured PATH-2 slot. The controller requires an exact
decoded mountinfo entry, a writable mount, and a real in-card userdata path. It
writes a versioned random 128-bit `card-id` through a flushed same-directory
temporary, renames it, then requires card `syncfs`. An abandoned temporary is
discarded; an existing valid identity is returned unchanged, while an invalid
one is never replaced. Success returns the same complete status result as
`status.get` with the reconciled card inventory.

The primary registry retains physical ID, last logical slot, and last measured
retained bytes so an absent card remains visible. It is display/reconciliation
metadata only: every write still requires the currently mounted card's own
matching `card-id`. A replacement card at the remembered mountpoint therefore
appears as a separate unenrolled row, and duplicate live IDs fail closed.

Generic Run, Stop, and Start-with-Leaf operations remain CTL-1. Folder, peer,
gateway, and reset operations will be added only with the controller model that
validates and executes them.

## `network.profile.set`

```json
{"v":1,"id":"network-lan","op":"network.profile.set","args":{"profile":"lan-only","confirmed":true}}
```

`profile` is exactly `lan-only` or `sync-anywhere`, and `confirmed` must be
`true`; the device UI owns the explanatory confirmation. LAN-only derives
`allowed_networks` from current directly connected physical-interface routes.
The controller applies the D-14 pause → policy update → unpause transition, and
keeps watching routes while it runs. Sync Anywhere clears the per-peer boundary
and enables global discovery, relays, and NAT traversal together. Success
returns the full status result with the applied network profile.

The canonical fixtures live in `tests/fixtures/ui-control-v1/`. `make test`
round-trips their exact JSON and framing in Go and C.
