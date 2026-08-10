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

The LIFE-1 extension adds an optional mode-stop `check_before_stop` exchange.
The controller checks only managed Saves/States on the launch's resolved card.
It reports current only when local need is zero and every selected peer is
configured, connected, unpaused, valid, and has zero remote need. Jawaka offers
Wait for sync, Play anyway, and Cancel. Every proceed path still runs and
verifies the existing supervisor stop before an emulator writer can start.

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
| Leaf-Syncthing-Pak | `go test -race ./internal/controller ./internal/life1 ./internal/syncthing` | Pass |
| Jawaka | `make life1-test jawakad jawaka-launcher` | Pass; only pre-existing third-party miniz prototype warnings |
| Jawaka | `make life1-game-check-ipc-smoke` | Pass: current, bounded wait, Play anyway, Cancel, expiry, timeout, and malformed reply |
| Jawaka | Existing LIFE-1 game/fallback/override/recovery smoke suites | Pass |
| Workspace/Jawaka | Contract validator and C wire-fixture test | Pass: 29 fixtures |

The mount-repair unit tests preserve sentinel data at both old and new roots
and refuse missing-marker and duplicate-card-ID cases without calling the
upstream mutation API. The API test requires an exact paused path and exact
same-card versioning response after the PATCH.

The final development package was reproducible across two assemblies:

| Artifact | Bytes | SHA-256 |
| --- | ---: | --- |
| Installed `Syncthing.pak` tree | 33,600,072 | — |
| `Syncthing.mlp1.pak.zip` | — | `f4cd8e8d619becafc1b9320da9db1478ccc8c8d48ff66051fbe62a643c33e9f9` |
| ARM64 `leaf-syncthing` | — | `f81881b9e0d871e60f893632d0569a67da65cf02b47c3e6f82c55992458dc273` |

The staged ARM64 Jawaka daemon and launcher hashes also matched their host
builds exactly. The package remained an unstamped `0.0.0-b4c` development
artifact; B5 still owns release version and compatibility-floor stamping.

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

## Remaining qualification

- Run the complete PC-first, Leaf-first, later-peer, Android/non-Leaf handheld,
  and second-Leaf journeys with real clients and transfer data.
- Exercise Wait / Play anyway / Cancel on the physical MLP1 during an active
  inbound transfer and directly prove service-group absence before its real
  emulator writer starts.
- Finish the single guided first-run screen journey and documentation handoff.
- Stamp and verify only the eventual B5 release artifacts; this checkpoint
  authorizes no tag, catalog mutation, or production release.
