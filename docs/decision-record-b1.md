# B1 controller qualification record

**Status:** In progress; early controller/upstream gate passed

**Date:** 2026-08-08

**Device:** MLP1 ADB serial `f40098e329c73533`

**Controller commit:** `79f9f228699e8c78da358fdec211679a179ff0c6`

**UI control commit:** `536eb457a3dfe5ccbd396dd87f839576d53a2594`

**Jawaka commit:** `f50d5ce745fdbcc7a65551d3783558d2ff1d0a7c`

**Contract pin:** `5ab17d82c122b481f15c835e6ff9a21829d45aa9`

This is the early production-shaped B1 measurement required before card and
folder work expands the controller. It is not the final B1 completion record.

## Implemented startup boundary

The cgo-disabled `leaf-syncthing` command now implements the first six ordered
`SYNC-1` startup steps before spawning the pinned upstream:

1. take the controller singleton under Jawaka's service runtime directory;
2. validate/create only the app-owned durable roots;
3. establish a close-on-exec LIFE-1 subscription and reconcile `game.state`;
4. recover `config.xml`/`.tmp`/`.bak` through the normative state table;
5. generate and validate identity in same-filesystem temporary config/data
   directories, flush, promote, and revalidate the certificate-derived device
   id and identity marker;
6. apply/reparse managed pause fields through the recoverable three-file
   transaction. B1 currently has no enrolled folders, so the live pause set is
   empty.

Factory generation applies the Leaf-owned profile while the config is still an
uncommitted staging tree. Its token rewrite preserves unknown XML while setting
the private GUI Unix socket and `0600` socket permissions, disabling global
discovery, relays, NAT traversal, usage/crash reporting, browser launch, and
auto-upgrade, and retaining local discovery/direct sync. Runtime also passes
`--no-upgrade`, `--no-restart`, and the current Unix GUI address.

The upstream runner verifies that no foreign Syncthing process or conventional
GUI listener exists, starts the monitor/main tree in the controller's reserved
process group with child `PDEATHSIG`, waits for an authenticated REST response
over the Unix socket, and checks the actual socket type/mode. It never restarts
upstream inside the same generation. Normal stop requests graceful API shutdown
and proves the remaining group empty; guardian cleanup signals every other
group member, escalates survivors, and proves non-zombie absence before the
controller returns.

After upstream readiness, the controller now serves the package-private UI
socket at `control.sock`, independently of the upstream admin socket. The
frozen v1 envelope, status shape, compatibility rules, and error vocabulary are
documented in `docs/ui-control-v1.md`; canonical fixtures under
`tests/fixtures/ui-control-v1/` round-trip from both Go and standalone C. B1
advertises only the implemented read-only `status.get` operation. Future
mutation operations remain absent until their controller-owned card/network
models exist.

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
  version and device id, empty card/folder rows, and only the implemented
  `status.get` capability. It disappeared after each CTL-1 Stop.
- CTL-1 Stop reached `disabled`/`stopped` and left no controller, monitor, or
  main process from the fixture.
- A second supervised Run reused byte-identical certificate, private-key, and
  generation-marker hashes. The second Stop again proved absence.
- A separate opt-in device test generated/promoted/revalidated the same
  transaction on the real FAT-backed `$USERDATA_PATH`, using real Linux
  `syncfs`; it then removed its exact test root and temporary binaries.

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

## Remaining B1 work

- implement card enrollment, mountinfo/card-id verification, managed folder
  reconciliation, retained-data inventory, and foreign-instance UI reporting;
- implement the existing-identity disposable-copy migration path;
- exercise controller death, direct-upstream death, and Jawaka death/restart
  guardian cases with an active upstream workload;
- repeat the relevant footprint and removal checks once real card bindings and
  folders exist.
