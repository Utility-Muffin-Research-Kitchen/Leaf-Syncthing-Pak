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
- The normal socket is available after controller bootstrap. During a durable
  reset that is waiting for a specific enrolled card, a recovery-only socket
  remains available with `status.get` as its sole capability. A missing socket
  means the controller is unavailable; the UI uses CTL-1 only for generic
  service status and Run/Stop/enable actions.

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

`args` must be `{}`. The controller returns:

```text
controller                         running | recovery-pending | error
upstream.state                     stopped | starting | running | error | conflict
upstream.version                   pinned Syncthing version
upstream.device_id                 certificate-derived device id
game.active/launch_id/source_id    reconciled LIFE-1 state
recovery.state/changed             ready | pending | error; whether startup recovery changed config
network.profile/allowed_networks   lan-only | sync-anywhere; current route-derived CIDRs
gateway                            foreground HTTPS/pairing/trust state, when available
transfer                           aggregate state, sizes, need, and session byte counters
logging                            normal | debug and the fixed debug expiry
storage                            bounded snapshot/version inventory and byte totals
diagnostics                        last fixed-path redacted export, if any
cards[]                            enrolled/configured physical-card rows
folders[]                          managed-folder rows
peers[]                            configured and pending peers with connection kind
folder_offers[]                    pending standard Syncthing folder announcements
issues[]                           display-safe controller/card/folder issues
capabilities[]                     supported operation names
```

Card rows freeze physical identity, current slot/root, state (`absent`,
`unenrolled`, `enrolled`, `invalid`, or `duplicate`), presence, writability,
duplicate-id state, retained bytes, and scoped issues. Folder rows freeze
identity, card/kind/path/type, pause state and reasons, sizes, peer count, exact
`device_ids` membership, last sync, versioning, an optional bounded conflict
list, and scoped issues. Peer rows
distinguish `local`, `direct`, `relay`, and `none`; pending introductions are
explicit and are never accepted by a status read. Folder-offer rows include
the network folder ID and label, offering device ID/name, offer time, and
encryption flags; status remains read-only and exposes at most 32 offers.
Counts and byte sizes are
non-negative; an empty timestamp is unknown. B3 still owns onboarding and
first-sync release, so a strict pre-existing Leaf binding remains paused for
`first-sync` until that phase's durable flow completes.

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

Generic Run, Stop, and Start-with-Leaf operations remain CTL-1.

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

## Gateway operations

The gateway is an optional, foreground-owned HTTPS view of a fixed read-only
subset of the pinned Syncthing web UI. It never exposes the upstream Unix
socket or its credentials directly. `status.get` includes a `gateway` object
with listener state, pairing state, URL, four-digit PIN, fragment-bearing QR
URL, offer expiry, certificate fingerprint, trusted-browser count, and any
extension expiry.

Opening and maintaining the foreground lease use empty arguments:

```json
{"v":1,"id":"gateway-open","op":"gateway.open","args":{}}
{"v":1,"id":"gateway-keepalive","op":"gateway.keepalive","args":{}}
{"v":1,"id":"gateway-close","op":"gateway.close","args":{}}
```

`gateway.open` binds one exact eligible LAN address, creates a two-minute
single-use pairing offer, and returns the full status. `gateway.keepalive`
renews the short foreground lease. `gateway.close` removes the listener unless
the user explicitly granted an extension after pairing a browser.

The destructive or longer-lived actions require an explicit confirmation:

```json
{"v":1,"id":"gateway-extend","op":"gateway.extend","args":{"confirmed":true}}
{"v":1,"id":"gateway-revoke","op":"gateway.revoke-all","args":{"confirmed":true}}
```

`gateway.extend` grants at most 15 minutes and requires an already trusted
browser. `gateway.revoke-all` removes all browser trust and closes the listener.
Route/address changes, lease expiry, controller shutdown, or network-profile
changes also close it.

## Folder operations

All folder ids must name an existing controller-registered Leaf binding. The
network id may have been created by Leaf or offered by a standard Syncthing
peer; its durable binding separately identifies the enrolled card, content
kind, and local safety marker. The UI never sends a path, and the controller
never exposes a free-form path mutation.

```json
{"v":1,"id":"folder-inspect","op":"folder.inspect","args":{"folder_id":"leaf-saves-0011223344556677"}}
{"v":1,"id":"folder-pause","op":"folder.pause","args":{"folder_id":"leaf-saves-0011223344556677"}}
{"v":1,"id":"folder-resume","op":"folder.resume","args":{"folder_id":"leaf-saves-0011223344556677"}}
{"v":1,"id":"folder-rescan","op":"folder.rescan","args":{"folder_id":"leaf-saves-0011223344556677"}}
{"v":1,"id":"folder-rename","op":"folder.rename","args":{"folder_id":"leaf-saves-0011223344556677","label":"Leaf Saves"}}
```

`folder.inspect` performs a bounded, symlink-rejecting scan of the validated
folder for Syncthing conflict copies and returns at most 64 display paths plus
the total count. The device UI combines this with the controller's same-card
snapshot/version inventory. Pause state is durable before the upstream pause
request. A rescan requested while paused is durable and queued. Resume refuses
while any non-manual reason remains, including B3's `first-sync` reason.

Creating a folder requires an explicit, non-empty peer selection:

```json
{"v":1,"id":"folder-plan","op":"folder.onboard.plan","args":{"source_id":"primary","kind":"saves","folder_type":"sendreceive","device_ids":["IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP"]}}
```

The controller checks that every selected ID is a unique configured peer both
when it creates the review and when it consumes the plan. Peers added after the
review are not silently included. The foreground UI presents the configured
peers as an Include/Exclude checklist before requesting the review.

## Folder sharing

An existing managed folder is shared or unshared with exactly one already
configured peer at a time:

```json
{"v":1,"id":"share","op":"folder.share","args":{"folder_id":"leaf-saves-0011223344556677","device_id":"IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP","confirmed":true}}
{"v":1,"id":"unshare","op":"folder.unshare","args":{"folder_id":"leaf-saves-0011223344556677","device_id":"IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP","confirmed":true}}
```

The foreground Sharing screen derives its checklist from the exact
`folders[].device_ids` list; adding a device never shares existing folders
automatically. The controller durably records the requested pair, pauses the
folder, patches and re-reads upstream configuration, clears the intent, and
then restores the safety-derived pause state. Startup completes an interrupted
intent before normal mutations become available. Unsharing the final remote
peer leaves the local managed folder and live files intact so it can be shared
again later.

## Folder-offer planning

`status.get` exposes pending offers without accepting them. To review one
against a selected enrolled card and local direction, the UI sends:

```json
{"v":1,"id":"offer-plan","op":"folder.offer.plan","args":{"folder_id":"retro-saves","device_id":"IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP","source_id":"primary","kind":"saves","folder_type":"sendreceive"}}
```

The controller re-reads live offers, rejects encrypted or vanished offers, and
returns the normal bounded onboarding review with `join_existing:true` and the
offering device ID. The existing confirmed `folder.onboard.create` operation
consumes that plan. Leaf retains the offered network folder ID but supplies its
own card path, type, custom marker, paused first-sync state, and same-card
versioning. Only the offering device is included; unrelated configured peers
are never silently added. A pending binding is flushed before the upstream add;
startup activates it if the paused upstream folder exists or rolls it back if
the add never happened, without deleting the live Saves/States tree.
The foreground Folders screen shows a pending-offer count, the offering device,
and an explicit card/content/direction review; encrypted offers are labeled as
unsupported and cannot enter the creation flow.

## Device operations

```json
{"v":1,"id":"device-add","op":"device.add","args":{"device_id":"AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH","name":"Laptop"}}
{"v":1,"id":"device-rename","op":"device.rename","args":{"device_id":"AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH","name":"Laptop"}}
```

The controller accepts a canonical device id or a `syncthing://` device URI.
New peers always use dynamic addresses, never become introducers, never
auto-accept folders, and inherit the current route-derived LAN boundary.
Pending devices are shown by status but require this explicit add operation.

## Logging and diagnostics

```json
{"v":1,"id":"debug","op":"log.level.set","args":{"level":"debug","confirmed":true}}
{"v":1,"id":"normal","op":"log.level.set","args":{"level":"normal","confirmed":true}}
{"v":1,"id":"diagnostics","op":"diagnostics.export","args":{}}
```

Debug logging expires after 15 minutes even across a controller restart.
Diagnostics always use the controller-selected
`leaf-syncthing-diagnostics.json` under `LOGS_PATH`; the caller cannot select a
path. The report contains bounded states, counts, byte sizes, versions, card-id
suffixes, and safe issue codes, but no API key, gateway secret, peer/device id,
folder path/name, game id, cookie, token, PIN, or private key.

## Reset preparation

```json
{"v":1,"id":"index-reset","op":"reset.prepare","args":{"action":"index-only","confirmed":true,"confirmation":"RESET INDEX"}}
{"v":1,"id":"full-reset","op":"reset.prepare","args":{"action":"full","confirmed":true,"confirmation":"RESET SYNCTHING"}}
{"v":1,"id":"available-reset","op":"reset.prepare","args":{"action":"available-only","confirmed":true,"confirmation":"RESET AVAILABLE STATE"}}
```

The response seals and displays the exact validated deletion set but changes no
durable data. The C UI asks CTL-1 to stop the service and waits for the owned
process group and lease to be absent before invoking the package's
`reset-execute` helper with the random plan id. That helper revalidates the live
card inventory, takes the controller lock, persists and syncs the exact durable
intent, removes only the declared controller/index/trust/history roots, syncs
each filesystem, verifies absence, and clears the intent last. Full reset
requires every enrolled card. Available-only names the absent-card roots it
retains. Saves, States, and ROMs are never valid reset roots.

The canonical fixtures live in `tests/fixtures/ui-control-v1/`. `make test`
round-trips their exact JSON and framing in Go and C.
