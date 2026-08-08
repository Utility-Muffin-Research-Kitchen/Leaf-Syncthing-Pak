# B1 controller qualification record

**Status:** In progress; controller/upstream, card, migration, folder-conflict,
guardian, write-boundary, package, and software-audit gates passed; physical
removal remains

**Date:** 2026-08-08

**Device:** MLP1 ADB serial `f40098e329c73533`

**Controller commit:** `79f9f228699e8c78da358fdec211679a179ff0c6`

**UI control commit:** `536eb457a3dfe5ccbd396dd87f839576d53a2594`

**Card safety commit:** `78ba51192cd254c67b0011a0387236783f7e4336`

**Existing-identity migration commit:** `a3db504fefcc283ba0b727f5b308c98f0ca76f60`

**Managed-folder/conflict commit:** `6e6318f0fd86d079e36c66c6bd19bb4da6a5533f`

**Transitive guardian commit:** `7425c10f3c5759956eb8bfdf3cbf889cf2cf491e`

**Write-boundary qualification commit:** `a263145a3403d7acf2cf401505b58286aff58d0d`

**B1 package commit:** `28fde1d048ef2280df42979f95213e242041ed59`

**Jawaka commit:** `f50d5ce745fdbcc7a65551d3783558d2ff1d0a7c`

**Contract pin:** `e9b00c5e357c32e5d4e055d1d10d5e7b6fff944c`

This is the early production-shaped B1 measurement required before card and
folder work expands the controller. It is not the final B1 completion record.

## B1 development package

`make package-mlp1` now builds the cgo-disabled controller rather than the B0a
gateway spike. The `0.0.0-b1` manifest carries the normative SVC-1 service and
retained-state declarations, and its service path resolves to the packaged
`bin/leaf-syncthing`. The deterministic ZIP contains exactly six regular,
FAT-safe files: controller, pinned upstream, foreground development launcher,
manifest, upstream license, and upstream lock/evidence. The B0a gateway remains
source-only qualification evidence and is absent from the package.

## Implemented startup boundary

The cgo-disabled `leaf-syncthing` command now implements the first six ordered
`SYNC-1` startup steps before spawning the pinned upstream:

1. take the controller singleton under Jawaka's service runtime directory;
2. validate/create only the app-owned durable roots;
3. establish a close-on-exec LIFE-1 subscription and reconcile `game.state`;
4. recover `config.xml`/`.tmp`/`.bak` through the normative state table;
5. generate and validate a factory identity in same-filesystem temporary
   config/data directories, or migrate an older existing config only through
   disposable copies; validate, flush, promote only `config.xml`, and recheck
   the certificate-derived device id and identity marker;
6. recognize strict Leaf-managed folder IDs, force every one paused, and force
   `setLowPriority=false` through the recoverable three-file transaction before
   spawn. B1 creates no folder; B3 owns onboarding and first-sync completion.

Factory generation applies the Leaf-owned profile while the config is still an
uncommitted staging tree. Its token rewrite preserves unknown XML while setting
the private GUI Unix socket and `0600` socket permissions, disabling global
discovery, relays, NAT traversal, usage/crash reporting, browser launch, and
auto-upgrade, and retaining local discovery/direct sync. Runtime also passes
`--no-upgrade`, `--no-restart`, and the current Unix GUI address. The explicit
`setLowPriority=false` prevents upstream's Linux priority helper from moving the
main process into a new process group; without it Jawaka's reserved group would
contain only the controller and monitor. This safety scalar is also enforced by
the only permitted committed-config offline transaction on every startup.

The upstream runner checks for a foreign Syncthing process or conventional GUI
listener before spawn. On conflict the controller remains available on its
private socket with a specific conflict issue and starts no upstream. Otherwise
it starts the monitor/main tree in the reserved process group with child
`PDEATHSIG`, waits for authenticated REST over the Unix socket, and checks the
actual socket type/mode. It never restarts upstream inside the same generation.
Normal stop requests graceful API shutdown and proves the remaining group
empty; guardian cleanup signals every other group member, escalates survivors,
and proves non-zombie absence before the controller returns. Device evidence
requires the controller, monitor, and main process to share the controller's
reserved process-group id.

After upstream readiness, the controller now serves the package-private UI
socket at `control.sock`, independently of the upstream admin socket. The
frozen v1 envelope, status shape, compatibility rules, and error vocabulary are
documented in `docs/ui-control-v1.md`; canonical fixtures under
`tests/fixtures/ui-control-v1/` round-trip from both Go and standalone C. B1
advertises read-only `status.get` plus explicit `card.enroll`; other mutation
operations remain absent until their controller-owned folder/network models
exist.

The controller now consumes PATH-2 rather than deriving hidden userdata paths.
It requires the complete v2 singular/plural set, byte-matched Primary aliases,
aligned counts/order, unique roots, and every content/userdata entry confined
to its declared card. Card inspection uses exact decoded Linux mountinfo for
both slots. Explicit `card.enroll` writes a versioned random 128-bit identity
through `card-id.tmp`, flushes the file, promotes it, and requires real card
`syncfs`; an existing identity is never replaced automatically.

A recoverable primary registry retains physical identity, last logical slot,
and last measured retained bytes for configured-but-absent display. Live writes
never trust that remembered slot. Replacement cards remain separate, and two
mounted cards with the same ID are both marked duplicate. The status response
now exposes per-card presence, writable/enrollment/duplicate state, physical-ID
suffix, slot, retained bytes, and bounded display-safe issues. Folder binding
names and mandatory non-default markers are derived and validated. Any existing
strict Leaf Saves/States binding is paused offline before spawn, then reconciled
against its physical card, PATH-2 path, supported type, writability, custom
marker, foreign `.stfolder`, and explicit same-card versioning. Rows and issues
are visible, but B1 still onboards no folder.

## Fault-injection results

Host fault injection now covers the remaining B1-owned `B-REC-01` boundaries:

- card enrollment discards both partial and fully formed `card-id.tmp` files
  instead of adopting them; a failure of the card-wide flush after promotion
  converges by returning the exact promoted id without drawing new randomness;
- a failure of the first clean-identity filesystem flush leaves no final
  config, and the next factory-clean attempt generates afresh; a failure of the
  post-promotion flush leaves a complete marked identity that recovery validates
  without invoking `syncthing generate` again or changing certificate, key, or
  marker bytes;
- the complete `config.xml`/`.tmp`/`.bak` state table covers every offline edit
  rename boundary, including convergence after recovery-flush failure. A newly
  injected failure after successful pause-config promotion reparses as ready
  and the repeated edit is a no-op rather than another transaction.

## Physical-device results

`scripts/adb-mlp1-b1-controller-smoke.sh` ran an isolated executable mock card
under the exact Jawaka daemon binary while the live `loong_pangu` process was
paused. Cleanup unmounted and removed the fixture, resumed the live daemon, and
verified that no fixture process or mount remained.

- Jawaka accepted the controller's kernel-credential-bound LIFE-1 subscription
  and the mandatory `game.state` reconciliation returned inactive.
- The service reached CTL-1 `running` with `coordination: subscribed`.
- Exactly one controller and two upstream Syncthing processes were present.
- The admin API existed only at
  `$UMRK_RUNTIME_PATH/services/org.umrk.syncthing/syncthing-gui.sock`, mode
  `0600`; no TCP listener existed on conventional GUI port 8384. The direct
  encrypted sync listeners on port 22000 are expected.
- The separate mode-`0600` `control.sock` returned the frozen v1
  `status.get` response with running controller/upstream state, the pinned
  version and device id, an unenrolled Primary card row, empty folder rows, and
  the implemented `status.get`/`card.enroll` capabilities. It disappeared after
  each CTL-1 Stop.
- The controller enrolled the isolated bind-mounted card through
  `card.enroll`, returned an `enrolled` Primary row, and reused the same
  `card-id` bytes on the second supervised Run.
- With a separate real pinned Syncthing monitor/main pair already live, Leaf
  started only its controller, reported `upstream.state=conflict` plus
  `foreign-syncthing` on `control.sock`, and did not add an upstream process.
  Stopping Leaf left the foreign pair untouched; fixture cleanup then removed
  it before normal startup.
- CTL-1 Stop reached `disabled`/`stopped` and left no controller, monitor, or
  main process from the fixture.
- A second supervised Run reused byte-identical certificate, private-key, and
  generation-marker hashes. Before that Run the fixture changed the isolated
  config and marker from schema 52 / upstream v2.1.2 to schema 51 / v2.1.1.
  The pinned `generate --config=... --data=...` subcommand upgraded only the
  same-filesystem `config.migrate.tmp` copy; the controller verified copied
  certificate/key hashes and device identity, promoted only the validated XML
  through `config.xml.tmp`/`.bak`, restored the original marker hash, removed
  both migration staging directories, and only then spawned upstream. The
  second Stop again proved absence.
- With the service stopped, the fixture replaced the complete pak directory,
  deleted the derived upstream data/index directory, restarted Jawaka for fresh
  manifest discovery, and ran the reinstalled service. Certificate, key,
  generation marker, and card-id hashes remained byte-identical; the
  certificate-derived device id therefore remained stable as the index rebuilt.
- The same restart carried an injected, initially unpaused, derivation-correct
  Leaf Saves send-only binding. Startup forced it paused through the offline
  transaction before spawn and reported only the `first-sync` reason. A third
  run after adding a default `.stfolder` kept it paused and reported the
  folder-scoped `foreign-folder-manager` error. No folder or marker was created
  by the controller.
- The destructive guardian matrix used the same managed Saves binding with
  2,000 files plus a bounded 50,000-write mutator. Each failure was injected
  only after the Syncthing main process accumulated active CPU ticks from the
  scan workload.
- Killing the controller left its monitor/main generation for Jawaka to stop
  and verify. Killing the direct monitor made the controller report failure
  and exit while Jawaka contained the surviving main process. In both cases
  all three old PIDs disappeared before exactly one new controller/monitor/main
  generation reached `running`; the observer found no overlap.
- Killing Jawaka exercised the controller-owned guardian. The fixture first
  restarted the private API to close its pooled connection, then removed only
  the isolated socket pathname so the cleanup deadline was observable. A new
  Jawaka daemon reported `stale-generation`; the old controller still held fd
  3 at the exact generation-lease path while both upstream processes lived,
  and no replacement controller existed. After the controller escalated and
  proved the old group absent, the fixture restored its isolated API flag and
  exactly one replacement generation started.
- A separate opt-in device test generated/promoted/revalidated the same
  transaction on the real FAT-backed `$USERDATA_PATH`, using real Linux
  `syncfs`; it then removed its exact test root and temporary binaries.

`scripts/adb-mlp1-b1-card-safety.sh` then exercised the two actual mounted vfat
cards (`/dev/mmcblk3p1` and `/dev/mmcblk1p1`) using only validated temporary
userdata roots named `.userdata/mlp1-b1-card-smoke`:

- both cards received distinct identities through the production enrollment
  transaction and real Linux `syncfs`;
- reversing their logical source order changed only the displayed slot while
  each identity stayed with its physical card;
- removing the test identity from one card and enrolling it again produced a
  replacement row while the registry retained the old card as absent;
- copying one test identity onto both cards marked both live rows duplicate;
- cleanup removed both exact temporary roots and the remote test binary, and
  the live `loong_pangu` process remained running.

## Ten-minute idle gate

Command:

```sh
B1_MEASURE_SECONDS=600 scripts/adb-mlp1-b1-controller-smoke.sh
```

The sample ran for 601 seconds with 119 five-second observations. It used the
production-shaped controller and pinned v2.1.2 monitor/main tree with no peers
or enrolled folders; the isolated data root was under the fixture's `/tmp`
tree. RSS is therefore the required early B1 process-cost gate, not a final
real-library footprint.

| Process set | Average RSS | Maximum RSS | CPU (% of one core) |
| --- | ---: | ---: | ---: |
| Controller | 7,060 KiB | 7,060 KiB | 0.1864% |
| Upstream monitor + main | 49,966 KiB | 51,612 KiB | 0.2413% |
| Combined | — | **58,672 KiB (57.3 MiB)** | **0.4277%** |

Verdict: pass. The 57.3 MiB maximum is below both the preferred 80 MiB budget
and the 120 MiB stop threshold, so no keep/optimize exception is required. On
the four-core MLP1, the combined idle CPU figure is about 0.107% of total CPU
capacity. The earlier B0a 104,300 KiB upstream result included configured
folders/peer activity and is not a like-for-like controller-overhead number;
the measured resident controller increment here is about 6.9 MiB.

## Software-only closeout audits

- `make test`, `go test -race ./...`, `go vet ./...`, standalone C fixture
  compilation, and shell syntax pass.
- The packaged archive contains no symlink, absolute path, traversal, or file
  outside its one `Syncthing.pak/` root; its manifest was freshly discovered
  and run by real Jawaka on the MLP1.
- A tracked-production-source scan found no private key, bearer/basic
  authorization header, or literal API-key secret class.
- Leaf's existing optional-package policy smoke passes and keeps Syncthing out
  of default `STAGE_APPS`, required bootstrap repos, and `managed_apps`.
  The actual August 7 Release A SD ZIP also contains no Syncthing pak, path, or
  manifest entry.
- The repository does not yet declare a license for the UMRK controller code;
  selecting that license is a maintainer decision before B1 can be finalized.

## Remaining B1 work

- select and package the UMRK controller license;
- repeat the physical scan/receive/rename/versioning removal matrix through the
  real controller, then record the final configured-folder footprint.
