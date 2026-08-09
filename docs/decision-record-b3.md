# B3 folder onboarding and lifecycle decision record

**Status:** Complete; review and merge pending

**Date:** 2026-08-09

**Candidate:** `agent/b3-folders-lifecycle`, stacked on B2 commit `013db5f`

**Device:** MLP1 ADB serial `f40098e329c73533`, base Leaf release `0.9.0`

This record follows
`umrk-workspace/plans/leaf-syncthing/phases/phase-b3-folders-lifecycle.md`.
Both physical cards were explicitly declared expendable. Device work was
confined to `/tmp/leaf-syncthing-b3`,
`/mnt/sdcard/.leaf-syncthing-b3`, and
`/media/sdcard1/.leaf-syncthing-b3`; the harness removed those roots and
resumed the live launcher on exit. API keys, device identities, peer secrets,
and raw logs are excluded from this record.

## Product result

B3 adds the guided Saves and warned States presets to the separate
C/Catastrophe UI. Each enrolled card receives an independent folder binding.
ROMs, app userdata, and advanced folder types remain absent. The controller
revalidates the selected physical card, PATH-2 path, custom marker, existing
bindings, content shape, peers, and current snapshot space before creating a
paused upstream folder.

Receive-capable folders use Simple Versioning with `keep=5`, `cleanoutDays=0`,
`cleanupIntervalS=3600`, and an exact same-card `basic` version path. They
cannot leave first-sync pause until a same-card copy, SHA-256 manifest,
snapshot records, and card `syncfs` barriers are complete; the user has
acknowledged hub versioning; and the user explicitly starts the merge. A
space failure offers Send Only or cancellation. The controller repeats the
space check at create time, so capacity disappearing after the review cannot
create a receive-capable folder. The snapshot preparation path checks again
before copying.

The durable completion document uses a same-directory temporary and rename,
then a second card-wide barrier before the persisted first-sync pause reason
is cleared. Crash recovery removes partial snapshots and temporary documents,
revalidates completed records, and restores protection when the marker is
missing. A monotonically increasing per-folder epoch prevents a snapshot from
an earlier receive period from satisfying a later Send Only to receive
transition.

Conflicts are listed without mutation and provide Rescan and Open Thing-File
actions. Thing-File lookup follows every `APPS_PATHS` source, and the UI warns
that a general file manager is not coordinated with Syncthing. Folder details
state that gameplay is stop-only and disclose B0b's measured costs: about
7.4 seconds before launch, about 0.8 seconds for control to return after play,
and about two minutes for a forced 25,000-file index rebuild. There is no
gameplay selector or prelaunch pending-check control.

## Host and artifact evidence

All commands ran from `Leaf-Syncthing-Pak` on 2026-08-09:

| Command | Result |
| --- | --- |
| `make test` | Pass: all Go packages, 14 frozen C/Go wire fixtures, and the C semantic client |
| `go vet ./...` | Pass |
| `go test -race ./...` | Pass |
| `bash -n scripts/adb-mlp1-b3-folders.sh` | Pass |
| `shellcheck scripts/adb-mlp1-b3-folders.sh` | Pass |
| `git diff --check` | Pass |
| `make package-mlp1` | Pass: cgo-disabled ARM64 controller, strict `-Wall -Wextra -Werror` C UI, pinned upstream, and package assembly |

Two consecutive package builds were byte-identical:

```text
archive: build/mlp1/Syncthing.mlp1.pak.zip
sha256: 5cf64f9bd17a6365bfbab4f7148fde595d2345d1fe6f7d0aa6ca41b8660ee318
archive bytes: 14084159
installed bytes: 33370465
entries: 9
```

The archive audit rejected absolute or traversing paths, symlinks, entries
over 100 MiB, and secret/controller-state filenames. It contains static,
stripped ARM64 `leaf-syncthing` and pinned Syncthing binaries plus a separate
stripped ARM64 PIE `leaf-syncthing-ui`. The runtime manifest declares B3
development version `0.0.0-b3`, `game: "stop"`, storage/suspend stops, and the
existing retained/revoked state roots.

## Two-card MLP1 evidence

The exact package and current Jawaka LIFE-1 binaries passed:

```text
ADB_SERIAL=f40098e329c73533 scripts/adb-mlp1-b3-folders.sh
B3_GAME_RESULT source=primary service_absent_during_writer=true managed_trees_unchanged=true restart_after_barrier=true
B3_GAME_RESULT source=secondary service_absent_during_writer=true managed_trees_unchanged=true restart_after_barrier=true
B3_FOLDER_RESULT primary_and_secondary_receive=true same_card_snapshots=true explicit_start=true sendonly_receive_reprotected=true states_warning=true foreign_marker_refused=true conflicts_preserved=true
PASS MLP1 B3 two-card folder onboarding, first-sync protection, conflicts, and stop-only gameplay lifecycle
```

The harness verified both mounts were distinct real MLP1 VFAT block-device
partitions. For Primary and Secondary it created a receive-capable Saves folder
through the private protocol, selected the row by folder id, and proved it was
paused with first-sync required. Direct early start failed. The upstream
configuration carried the exact same-card version path/type and retained five
versions. Snapshot headers and manifests were present, the copied file hash
matched its source, and no completion marker existed before explicit start.
After start the durable marker existed and upstream became unpaused.

The same run refused a default `.stfolder`, required both warning
acknowledgments, created warned States only as Send Only, refused a duplicate
card/kind binding, and rejected reuse of the old snapshot after a
receive-to-Send-Only-to-receive transition. A generated conflict was surfaced
and remained on disk.

Game launches from both physical cards held the writer process live while the
harness directly proved that both controller and upstream were absent and the
control socket was gone. User Run returned `lifecycle-in-progress`; hashes of
both managed card trees remained unchanged. The service appeared only after
the writer-exit barrier. B0b's exact-device restart/reboot record supplies the
companion immediate-Jawaka-replacement and fresh-post-reboot spawn-gate cases
for `B-GAME-02`.

## Verification disposition

| ID | B3 evidence |
| --- | --- |
| `B-FOL-01` | Host ENOSPC/space-race tests, strict acknowledgments, crash tests, and both real-card paused/snapshot/versioning/start cases pass. |
| `B-FOL-02` | Snapshot and completion fault matrices cover every named boundary; partial cleanup and both card barriers pass. |
| `B-REC-01` | B3 completion-marker recovery matrix passes; B1/B2 retain ownership of enrollment, identity/config, and reset portions. |
| `B-SYN-03` | The real-card default-marker onboarding refusal passes; B1 retains the foreign-process startup half. |
| `B-LIF-04`, `B-GAME-02` | Real Syncthing is absent during both card launches and cannot be started; B0b supplies immediate daemon replacement and forced-reboot cases. |
| `S-FOL-02`, `S-FOL-03` | Independent presets, warnings, duplicate/foreign guards, transition re-protection, and non-destructive conflict reporting pass. |
| `S-GAME-01`, `S-GAME-02` | Both card launches prove stop-only behavior and tree stability; fixed cost copy is present and forbidden controls are absent. |

`O-REC-01` and `O-REC-02` were not run; they remain opportunistic and must be
listed among known-unqualified areas if still absent at B5. The safety snapshot
is intentionally documented as durable best-effort rather than atomic or
crash-consistent when another writer changes the source during the copy.

## Handoff

B5 receives the qualified Saves/States surface, first-sync recovery behavior,
stop-only cost copy, conflict workflow, two-card evidence, and the explicit
opportunistic gaps above. B4a and Release P remain separate prerequisites; no
version tag or production catalog mutation is authorized by this record.
