# B4c multi-device implementation checkpoint

**Status:** Implementation and interoperability qualification complete; B5 handoff pending

**Date:** 2026-08-11

**Candidates:** `Leaf-Syncthing-Pak` branch
`agent/syncthing-multi-device`; `Jawaka` branch
`agent/syncthing-check-before-stop`

**Devices:** Two physical MLP1 units, two enrolled physical VFAT cards on the
first unit, an unmodified standard Linux Syncthing peer, and an Android 14
handheld running Syncthing-Fork 2.1.3.0

This checkpoint follows
`umrk-workspace/plans/leaf-syncthing/phases/phase-b4c-multi-device-interoperability.md`.
It records implemented and directly verified behavior through the complete
required client matrix. The public setup and recovery guide remains B5 work.

## Implemented result

B4c now separates the shared Syncthing folder ID from Leaf's local physical
card binding. Existing folders migrate in place, incoming standard Syncthing
folder offers can be reviewed and adopted, folder membership is explicit, and
later share, unshare, device removal, local stop, retained-history cleanup, and
first-run status are controller-owned and recoverable. Live Saves/States are
not deleted by local stop or cleanup.

The foreground journey now names its exact resumable action on the overview:
enroll or fix a card, connect a device, set up Saves, finish first sync, wait
for syncing, or fix an issue. An already enrolled but absent/read-only card is
no longer described as needing enrollment. When several physical cards have
independent Saves shares, the journey asks which durable card-bound share to
continue instead of selecting one by array or mountpoint order.

The LIFE-1 extension adds an optional mode-stop `check_before_stop` exchange.
The controller checks only managed Saves/States on the launch's resolved card.
It reports current only when local need is zero and every selected peer is
configured, connected, unpaused, valid, and has zero remote need. Jawaka offers
Wait for sync, Play anyway, and Cancel. Every proceed path still runs and
verifies the existing supervisor stop before an emulator writer can start.
The first result retains the 250 ms acknowledgement budget. After pending work
has been reported, bounded follow-up reads may take two seconds and a transient
read failure retains the last known pending result and retries within Jawaka's
existing 15-second Wait deadline. Progress is refreshed at 500 ms rather than
logging byte changes ten times per second. An initial unreadable status still
fails closed immediately, and persistent trouble still expires into the same
safe Play-anyway-or-Cancel decision.

Card mountpoints are treated as runtime observations. At startup, an upstream
folder whose stored path no longer matches its bound card is already paused by
offline reconciliation. The controller then relocates only its local content
path and same-card version path when the durable card ID resolves uniquely and
the expected custom marker validates at the new PATH-2 path. It verifies the
paused upstream result before recomputing safety and unpausing. It never copies,
moves, or deletes live Saves/States. Missing markers, duplicate IDs, unavailable
or read-only cards, and unsupported folders remain paused.

## Host evidence

The following passed after the implementation and mount-repair changes:

| Repo | Command | Result |
| --- | --- | --- |
| Leaf-Syncthing-Pak | `make test` | Pass: all Go packages and C UI-control fixtures/client |
| Leaf-Syncthing-Pak | `go vet ./...` | Pass |
| Leaf-Syncthing-Pak | `go test -race ./internal/controller` | Pass after the physical-device follow-up fixes |
| Jawaka | `make life1-test jawakad jawaka-launcher` | Pass; only pre-existing third-party miniz prototype warnings |
| Jawaka | `make life1-game-check-ipc-smoke` | Pass: current, bounded wait, Play anyway, Cancel, expiry, timeout, and malformed reply |
| Jawaka | Existing LIFE-1 game/fallback/override/recovery smoke suites | Pass |
| Workspace/Jawaka | Contract validator and C wire-fixture test | Pass: 29 fixtures |

The mount-repair unit tests preserve sentinel data at both old and new roots
and refuse missing-marker and duplicate-card-ID cases without calling the
upstream mutation API. The API test requires an exact paused path and exact
same-card versioning response after the PATCH.

The pre-qualification development package was reproducible across two
assemblies:

| Artifact | Bytes | SHA-256 |
| --- | ---: | --- |
| Installed `Syncthing.pak` tree | 33,600,072 | — |
| `Syncthing.mlp1.pak.zip` | — | `f4cd8e8d619becafc1b9320da9db1478ccc8c8d48ff66051fbe62a643c33e9f9` |
| ARM64 `leaf-syncthing` in that package | — | `f81881b9e0d871e60f893632d0569a67da65cf02b47c3e6f82c55992458dc273` |

After the focused physical fixes, the rebuilt ARM64 controller hash was
`808bd7effa4513aef24613064afac64c42d6c8a150bb68ddabab86b1de373abe` and
the rebuilt Jawaka daemon hash was
`7f43415934988df28259f0d131afa606ecd3c55c0c3e359e46693b2131fe9e01`.
Each installed device file matched its host build exactly. The package remains
an unstamped `0.0.0-b4c` development artifact; B5 still owns the final package
rebuild, release version, and compatibility-floor stamping.

## Actual two-card reboot evidence

Before repair, the bound card ID `1d27f6e6…3f6923a5` was mounted at
`/mnt/sdcard`, while the other enrolled card was `/media/sdcard1`. The persisted
upstream paths still named the latter, so startup correctly exposed both
folders as paused with unsafe path/versioning issues. Staging the candidate
repaired `ra-saves` and `ra-states` to `/mnt/sdcard/Saves` and
`/mnt/sdcard/States`, with version history under the same card's
`.userdata/mlp1/Syncthing/versions/{saves,states}`. Both folders returned idle,
unpaused, and remotely current with the standard VPS peer connected.

A real reboot through Jawaka's kernel reboot path then exchanged the two block
devices and mountpoints: the same Leaf card moved to `/media/sdcard1` and the
other card moved to `/mnt/sdcard`. Without manual config edits, the controller
followed the same durable card ID and repaired both folder and version paths
back to `/media/sdcard1`. After network reconnection, both folders again
reported `remote_state:"current"` with no issues.

Before and after both repairs, the Leaf card contained only its expected Saves
and States marker directories, while the other card retained its independent
`Stella 2014` Saves and States directories and received no Leaf marker. The
network folder IDs, device identity, peer membership, first-sync snapshots, and
version stores remained unchanged. The post-reboot Jawaka log also confirmed a
live subscription with `check_before_stop=true`, `ack_ms=250`, and
`wait_ms=15000`.

## Physical check-before-stop evidence

The real MLP1, real RetroArch writer, and standard Syncthing v2.1.2 VPS peer
qualified every user choice with active inbound data:

- **Cancel:** with one 128 MiB Saves item pending, Cancel cleared the
  known-not-started launch, left the Syncthing service running, started no
  RetroArch process, and allowed the transfer to continue to current.
- **Wait expiry:** a separate 128 MiB transfer used the full 15-second budget
  and returned Needs attention with 12,701,696 bytes still pending. No writer
  started. Choosing Play anyway from that result still verified the service
  stopped before RetroArch.
- **Multiple peers:** the VPS and a temporary unmodified upstream Syncthing
  peer were both explicitly shared with `ra-saves`. A 16 MiB source item
  appeared as 33,554,432 aggregate pending bytes, reached all peers current in
  10,255 ms, and exposed the controller follow-up timing and deferred-writer
  cleanup bugs fixed in this checkpoint.
- **Successful Wait:** with the VPS sender temporarily capped to 4 MiB/s, one
  32 MiB inbound item was shown immediately, became current in 10,023 ms, and
  reached supervisor-verified stop 9,998 ms after Wait was selected. Only then
  did RetroArch start, with the complete Syncthing service group absent.
- **Direct Play anyway:** with another 32 MiB item pending, Play anyway
  cancelled the freshness check, verified the service stopped, and only then
  started RetroArch. On game exit, `game.finish` cleared the durable launch
  record, Syncthing returned subscribed, and the interrupted transfer reached
  current.

The final Wait run deliberately reproduced a two-second Syncthing status stall
near file finalization. The controller retained its last truthful pending
result, retried, observed current, and stayed inside the 15-second user budget.
Jawaka's deferred writer path now records the writer start, so the post-game
barrier clears `active-game.json` and releases the lifecycle stop instead of
leaving synchronization disabled.

Qualification cleanup restored the VPS bandwidth options to their original
unlimited values, removed only the exact test live/version files on both peers,
removed the temporary ROM and its library row, and left both managed folders
idle/current. Leaf reported zero retained version bytes, no active game, one
connected direct VPS peer, and no issues.

## Guided-status device evidence

The final MLP1 foreground binary (`SHA-256`
`b97b446b47c592cbca2f19b1d5280e71a19d983deb0990324a269ae23df53e1e`)
matched its staged file byte-for-byte. On the existing configured two-card
device, a native 960x720 framebuffer capture showed `Start with Leaf: On`,
`Guided setup: Complete`, and `Status: Up to date`, with two cards, the Saves
and States folders, and the standard VPS peer still present. Exiting the UI
returned to Jawaka cleanly (`app exited status=0`); CTL-1 then still reported
the Syncthing service enabled, running, and LIFE-1 subscribed.

This was the initial non-destructive resume/completion pass. The following
factory-clean Create and Join qualification closed that remaining device gap.

## Factory-clean Create and Join evidence

A second physical MLP1 was reset to a new Syncthing identity and driven through
the actual 960x720 controller UI. The clean journey started with the service
disabled, enabled **Start with Leaf**, enrolled the intended card, displayed
Leaf's device ID and QR code, and required explicit acceptance and naming of
pending peers. No browser administration or manual Syncthing configuration was
used for the Leaf side.

The **Create a Saves share** path selected the Primary card, selected both the
standard Linux hub and the first MLP1 in the peer checklist, chose **Merge
both**, and showed the exact card, path, content kind, direction, and peers
before creation. The folder was created paused, a same-card safety snapshot was
made, hub versioning was explained, and synchronization began only after the
second explicit confirmation. A unique fixture transferred to the unmodified
Linux peer with an identical SHA-256 digest.

The device was then fully reset again without changing the live Saves or States
trees. The Linux Syncthing v2.1.3 peer offered a new folder, and the actual
**Join an existing Saves share** path showed the offer and source device before
the user selected the card, Saves, direction, and exact final review. Leaf kept
the offered network folder ID while supplying `/media/sdcard1/Saves`, its own
physical-card binding, marker, first-sync pause, snapshot, and same-card
versioning. Transfers in both directions completed with matching SHA-256
digests. The UI ended at **Guided setup: Complete** and **Status: Up to date**,
with the selected peer reporting current and zero needed bytes.

The same UI pass also confirmed the separate **Create** versus **Join** choice,
the warned States opt-in, multi-peer selection, the File explorer first-sync
warning, and the resumable prepare/start/completion states.

## Second-Leaf same-folder evidence

The first MLP1 then shared its existing empty `ra-saves` network folder with
the clean second MLP1. The second unit accepted that exact offer through the
same controller onboarding operations, prepared its first sync, and reached
current over a direct local connection.

Both devices reported the same network folder ID, `ra-saves`, while retaining
different enrolled card IDs (`1d27f6e6…3f6923a5` and
`edf642e0…9bafd10e`), different first-sync snapshots, and independent local
paths and version stores. The first MLP1 simultaneously retained the standard
Linux peer on the same folder. Both Leaf devices reported idle/current, zero
needed bytes, and no controller issues. This is physical evidence that the
one-local-folder-per-`(card-id, kind)` safety rule does not prevent multiple
Leaf devices from joining one shared network folder.

## Second-device reboot and cleanup evidence

A reboot through Jawaka's kernel action restarted the second MLP1, autostarted
the enabled Syncthing service, restored the correct card-bound folder path, and
returned the standard peer and folder to current. This particular reboot did
not exchange its mountpoints; all pre/post live-tree hashes were identical.
The earlier first-device reboot in this record remains the physical
mountpoint-swap qualification and proved automatic relocation by durable card
ID.

Qualification cleanup used the normal unshare, local-stop, device-removal,
service-stop, and full-reset paths. The first MLP1 was restored to only its
original Linux peer with `ra-saves` and `ra-states` idle/current. The second
MLP1 finished disabled, with its generated identity/configuration/index absent
and its live Saves/States trees empty. A 19-file post-Join evidence and rollback
copy remains under the second card's qualification directory. Only the exact
temporary Linux qualification folder was removed from the standard peer.

## Android handheld evidence

A fresh Syncthing-Fork 2.1.3.0 install on a physical Android 14 handheld was
driven through its actual 640x480 UI over wireless ADB. First-run onboarding
granted background and notification permission, left optional location access
unrequested, generated a new identity, and started Syncthing under the normal
unmetered-Wi-Fi run conditions.

The Android client discovered the first MLP1 through standard local discovery
and explicitly added it without manual addresses. Leaf exposed the Android
device as pending, accepted it by device ID, and shared only the existing
`ra-saves` folder. `ra-states` retained only its original Linux peer. Android's
folder-offer notification named the offering Leaf and folder; opening it led to
the normal Create Folder review. The user selected
`/storage/emulated/0/LeafQualification/ra-saves` through Android's Storage
Access Framework, retained **Send & Receive**, and explicitly included the
Leaf device. The folder reached **Up to Date** while the first MLP1 retained
both the Android local peer and the direct Linux peer.

Two 91-byte fixtures then qualified the real data path in opposite directions:

| Direction | SHA-256 |
| --- | --- |
| MLP1 to Android | `8152219b167db5de1c54bc615389799161dd8b382aa9edfa28acbe2c7b0afbbf` |
| Android to MLP1 | `9d85974dd7881c4fb20aa5b4dba016e76786de2f63e08a2a17f954a1aa2505bf` |

Each digest matched at its source and destination. Leaf reported 182 local and
global bytes, zero needed bytes, two selected peers, idle/current, and no
issues. Syncthing-Fork independently displayed two files, 182 local/global
bytes, and **Up to Date**.

Using Syncthing-Fork's foreground controls, a forced stop made Leaf truthfully
report the Android peer offline and the multi-peer folder not current. A forced
start reconnected locally and returned the same folder to current without
losing configuration or data. Returning to **Follow run conditions** left the
service running under its ordinary Wi-Fi policy.

Android's standard Remove Folder action removed the Syncthing binding while
both fixture files remained byte-identical in the live directory. Cleanup then
removed the folder and peer from each client and deleted only the isolated test
trees. The first MLP1 returned to its original Linux-only `ra-saves` and
`ra-states`, both idle/current with zero bytes and no issues; the unrelated
second-card `Stella 2014` tree was untouched. Android retained its installed
app, identity, and onboarding permissions but no folders or devices.

## Focused foreground UX evidence

Catastrophe PR #4 added the optional options-list timer at merge commit
`6dbf3adaad42b20e11ef22d06d5ecef8ec63e25f`. Its native headless smoke
returned `CAT_ACTION_REFRESH` after the requested interval while retaining a
focused off-screen row and its scroll position. All six native examples built
before merge. This Syncthing branch pins that exact commit in CI.

The final foreground UI (`SHA-256`
`bb760ef3cc93bb005cac9d58d09d45a9550d4c343d7880a07e006b501b69fe27`)
and controller (`SHA-256`
`6bc7ff734e70a9e4eb0e066ce7c495e8d1484649ee4a831992b09de548139a20`)
were staged byte-for-byte to the first physical MLP1. The active Leaf card was
resolved by its platform marker at `/mnt/sdcard`; `/mnt/external_sd` was only a
symlink to that same mount and the second physical card had no Leaf marker.

The native 960x720 UI opened while the service was stopped with **Stopped**,
**Run Syncthing**, **Guided setup: Starts service**, and no socket error. The
Service row explained the stopped state and next action. Selecting Run showed
**Starting…** and then **Running** without another input; the same logical
service-action row remained focused as the status, cards, folders, devices,
network, browser, settings, and issues rows appeared. Selecting Stop showed
**Stopping…** and returned to **Stopped** without a controller-socket error.
Separate CTL-1 Run and Stop requests made while the overview was foreground
were likewise reflected automatically. The configured two cards, Saves and
States folders, original Linux device, zero-byte transfer state, and
**Up to date** summary remained unchanged.

The overview row and screen both read **Read-only web view**. The device screen
put **Status only—make changes on the handheld** before the HTTPS address and
PIN, and wrapped the certificate fingerprint into the available text column.
An eight-second idle check proved the foreground screen continued its
one-second gateway keepalive instead of closing at the four-second gateway
deadline.

A real client paired by PIN and a separate headless Chrome profile paired
through the fragment-only QR token. The pairing page contained the read-only
warning. Chrome rendered the unmodified Syncthing dashboard and its visible
upstream controls beneath one persistent banner reading **Read-only Leaf
status view. Make changes on the handheld.** With an explicit gzip request,
the trusted root returned 200 with exactly one banner, identity encoding, and
a corrected 77,542-byte `Content-Length`; HEAD returned 200 without body
decoration. A real POST to `/rest/config` returned 405 with **The Syncthing
browser view is read-only.**

Qualification cleanup revoked both temporary browser trusts, closed the
gateway, removed the temporary virtual input device and its staging files,
restored the original package launch script, and returned CTL-1 to its original
enabled/running state. No Saves or States content was written or removed.

## Guided-setup UX follow-up evidence

The follow-up foreground UI used for the physical flow (`SHA-256`
`94922649dfde5c0fea95f6816086691505cd9acf5c805e84ccc73cda80882906`)
was followed by a wording-only rebuild that adds the explicit offline, paused,
and not-sharing next actions (`SHA-256`
`11849eef5bae35c79a385b6e09180f5174b21ee532a9b417f9141c1081d24d2f`).
It was packaged with the unchanged controller (`SHA-256`
`6bc7ff734e70a9e4eb0e066ce7c495e8d1484649ee4a831992b09de548139a20`)
and upstream Syncthing (`SHA-256`
`f08f04f42c25f26fe68febfd8e8b777918b17da8011195317bbb8a0cc3a92e97`).
The final reproducible development archive hash was
`4167066de550f3574c130fe8314b250ddef1b05697c09f3b6193d61b90942a20`.
The final installed files on the SSH MLP1 matched those host artifacts exactly.

Host qualification passed `make test`, `go vet ./...`,
`go test -race ./internal/controller ./internal/syncthing`, and the MLP1
controller/UI/package build. The C semantic client covers incomplete and
first-sync-complete ordering, one or multiple pending offers, deliberately
ignored offers, and distinct offline, paused, not-sharing, and unknown remote
summaries. The exact `Thing-File.pak` lookup remains internal; all foreground
copy and supporting records now call it **File explorer**.

The existing configured device first proved the non-transient completion rule.
Its Saves folder had completed first-sync protection while the configured VPS
peer was offline and zero controller issues existed. The real 960x720 overview
placed **Guided setup: Fix issue** last, displayed **vranken-vps is offline for
Leaf Saves — Primary** directly on Status, omitted the Issues row, and showed
**Read-only web view**. This attention state did not incorrectly move Guided
setup back to the first position.

After the supported full reset, card enrollment remained present while the
Syncthing identity, configuration, indexes, peers, folders, snapshots, and
version history were absent. The stopped overview focused **Guided setup** as
its first row and showed **Starts service**, **Stopped**, **Start with Leaf:
Off**, and **Run Syncthing** without an error. Starting Guided setup used the
unnumbered **Connect a device** progression. While its live Devices screen was
open, adding the known VPS ID locally through the supported controller
operation made that guided-only screen return automatically. Normal VPS
configuration was not changed.

Because neither VPS folder was shared to this new Leaf identity, the journey
then advanced directly to the live **Set Up Saves** screen. It kept **Waiting
for folder offer — Share it to Leaf on the other device** first and **Create
new Saves folder instead** second while polling. No alternative folder ID was
created. This is the expected no-offer behavior for the confirmed VPS state;
the host semantics cover the complementary offered-folder and `Review offer`
paths.

A second supported full reset completed the qualification. The device was left
with the new package installed, service disabled, no controller socket or
Syncthing processes, and no generated identity/configuration/index, peers,
folders, snapshots, or versions. Its original card identity and card registry
remain enrolled for the next tester. Pre/post SHA-256 manifests of every
regular file below both physical cards' Saves and States roots were identical
(both manifests hash to
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`).
Only the temporary input wrapper, staging tree, and previous package copy were
removed during cleanup.

## Remaining handoff

- Exercise the public B5 setup, add/remove-later, conflict-recovery, and
  per-platform path instructions with representative users and clients.
- Stamp and verify only the eventual B5 release artifacts; this checkpoint
  authorizes no tag, catalog mutation, or production release.
