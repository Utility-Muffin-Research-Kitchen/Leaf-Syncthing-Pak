# B4c multi-device implementation checkpoint

**Status:** Implementation in progress; full interoperability qualification pending

**Date:** 2026-08-10

**Candidates:** `Leaf-Syncthing-Pak` branch
`agent/syncthing-multi-device`; `Jawaka` branch
`agent/syncthing-check-before-stop`

**Device:** MLP1 ADB serial `f40098e329c73533`, two enrolled physical VFAT
cards, standard Syncthing v2.1.2 peer

This checkpoint follows
`umrk-workspace/plans/leaf-syncthing/phases/phase-b4c-multi-device-interoperability.md`.
It records implemented and directly verified behavior without claiming the
remaining desktop, Android/non-Leaf handheld, or second-Leaf qualification
matrix.

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

This is non-destructive resume/completion evidence, not a substitute for the
remaining factory-clean Create and Join journey qualification.

## Remaining qualification

- Run the complete PC-first, Leaf-first, later-peer, Android/non-Leaf handheld,
  and second-Leaf journeys with real clients and transfer data.
- Run factory-clean physical Create and Join passes through the guided journey,
  then finish the documentation handoff.
- Stamp and verify only the eventual B5 release artifacts; this checkpoint
  authorizes no tag, catalog mutation, or production release.
