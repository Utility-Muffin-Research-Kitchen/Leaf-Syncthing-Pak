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

The frozen error codes are `bad-request`, `unsupported-version`,
`unsupported-op`, `bad-arguments`, and `internal`. Messages are display-safe
and never contain API keys, certificates, tokens, config contents, or peer
secrets. An unusable request id is returned as an empty string.

Request objects are strict. Response result objects are append-only: a v1
client must ignore unknown result members and use `capabilities` before showing
an action. Adding an operation or optional result member is compatible;
removing a member, renaming one, changing its type/meaning, or changing framing
requires v2.

## `status.get`

`args` must be `{}`. The current B1 controller advertises only `status.get` and
returns:

```text
controller                         running | recovery-pending | error
upstream.state                     stopped | starting | running | error | conflict
upstream.version                   pinned Syncthing version
upstream.device_id                 certificate-derived device id
game.active/launch_id/source_id    reconciled LIFE-1 state
recovery.state/changed             ready | pending | error; whether startup recovery changed config
cards[]                            enrolled/configured physical-card rows
folders[]                          managed-folder rows
issues[]                           display-safe controller/card/folder issues
capabilities[]                     supported operation names
```

Card rows freeze physical identity, current slot/root, presence, writability,
duplicate-id state, retained bytes, and scoped issues. Folder rows freeze
identity, card/kind/path/type, pause state and reasons, sizes, peers, last sync,
versioning, and scoped issues. Counts and byte sizes are non-negative; an empty
timestamp is unknown. B1 returns empty card/folder/issue arrays until enrollment
and reconciliation land.

Generic Run, Stop, and Start-with-Leaf operations remain CTL-1. No v1
Syncthing mutation is advertised yet: card, folder, peer, network, gateway, and
reset operations will be added only with the controller model that validates
and executes them.

The canonical fixtures live in `tests/fixtures/ui-control-v1/`. `make test`
round-trips their exact JSON and framing in Go and C.
