# B0b lifecycle timing and crash-race decision record

**Status:** Complete; review and merge pending

**Date:** 2026-08-08

**Device:** MLP1 ADB serial `f40098e329c73533`, base Leaf release `0.9.0`

This record follows
`umrk-workspace/plans/leaf-syncthing/phases/phase-b0b-integration-timing.md`.
Both physical cards were explicitly declared expendable. The fixture used only
`.leaf-syncthing-b0b` roots on those cards and `/tmp/leaf-syncthing-b0b`; raw
API keys, certificates, and device identities are excluded.

## D-1 verdict: ship stop only

The pause candidate used Syncthing's supported private REST folder-config
`PATCH` while a real 128 MiB inbound FAT transfer was active. The request did
not complete inside LIFE-1's frozen 250 ms acknowledgment allowance, so the
controller could not send `ready` within the 300 ms product budget. Jawaka
therefore took its required verified-stop fallback; the game writer began
7.119 s after the launch-record timestamp.

This rejects cooperative pause for the first release. No unsafe or slow pause
implementation remains in production code: both the controller subscription
and `pak.json` declare `stop`, and B3 must expose stop as the sole gameplay
behavior. Because the rejected exchange has no safe terminal action after an
optional pending check, B3 also defers “check for newer saves before launch”
until LIFE-1 explicitly defines and qualifies a stop-after-check result.

The completion query itself was cheap and remains useful future evidence. Ten
fresh Unix-socket HTTP connections per case measured:

| Peer state | Average | Maximum |
| --- | ---: | ---: |
| Online | 19 ms | 23 ms |
| Offline | 24 ms | 29 ms |

The clean 128 MiB stop-only qualification measured:

| Quantity | Result |
| --- | ---: |
| Launch record to game writer (conservative upper bound) | 7,340 ms |
| Game writer completion to controller control socket ready | 793 ms |
| Cold start plus full scan, 5,000 files | 19,426 ms |
| Cold start plus full scan, 25,000 files | 120,489 ms |

The stop path remained inside the manifest's 10 s graceful-stop window. After
the controller and both upstream processes were absent, the fixture stopped
the game writer, hashed every top-level Saves file's name/size/block/time
snapshot, waited 500 ms, and observed an identical hash. The transfer resumed
only after the writer-exit barrier and verified service restart, then converged
to the full 134,217,728-byte file.

B3 UI copy must round these measured costs honestly: stopping sync can add
about 7.4 s before a game starts; the control service returned in about 0.8 s
after play, while a forced 25,000-file index rebuild took about two minutes to
become idle.

## D-2 restart and reboot results

Stop-only removes the controller/upstream-mid-game state entirely: Jawaka
proves the supervised group absent before the game writer starts. While the
writer was deliberately held live:

- controller, Syncthing monitor/main, GUI socket, and package control socket
  were all absent;
- a user `Run` returned `lifecycle-in-progress` and created no process;
- killing Jawaka and starting a replacement recovered the tmpfs active-launch
  record before service autostart; another `Run` remained blocked and no
  controller or upstream appeared;
- a forced `reboot -nf` removed the writer, lease holders, and tmpfs launch
  record. The cards swapped mountpoints, but persistent role markers resolved
  the intended Primary and Secondary correctly;
- the post-reboot fixture wrapped the real pinned upstream binary with a spawn
  gate. Any invocation before the replacement Jawaka log contained a fresh
  `game.state active=false` reconciliation would have left a violation marker
  and exited 97. No marker appeared, and exactly one service generation
  reached running afterward.

B1 already qualified controller death, direct upstream-monitor death, and
Jawaka death under an active scan: every old controller/monitor/main PID became
absent before exactly one replacement generation appeared. Under B0b's
stop-only verdict those processes cannot exist once a game is active, so the
relevant in-session race is a restart or user Run attempting to recreate them;
the device cases above prove that gate remains closed.

The controller now reconnects a dropped LIFE-1 subscription and always obtains
a new `game.state` before continuing. Host tests cover inactive reconnect and
the fail-safe mode-stop case where a reconnect reports an active launch and the
controller shuts upstream down. Stale `game.finish` IDs remain ignored, and
`game.cancel` no longer clears authoritative active state. With no game pause
reason in the shipped mode, a lost finish cannot leave a folder stuck paused.

## Forced-cut FAT observation

The first forced-cut run remounted the then-Primary FAT volume read-only.
`fsck.vfat -a` cleared the dirty bit, reclaimed one unused cluster (32,768
bytes), corrected the free-space summary, and truncated several interrupted
fixture files whose FAT chains were gone, including `config.xml` and derived
index WAL/SHM files. The lifecycle state itself still reconciled fresh
`active=false`; service availability correctly remained blocked by storage
recovery.

The harness now recognizes stable card roles after a mount swap, detects
read-only mounts, accepts only the two exact MLP1 block-device partitions for
automatic `fsck.vfat`, and restores only its disposable pre-cut fixture backup
before continuing. A later complete forced-cut run needed no repair and
reported:

```text
B0B_RACE_RESULT daemon_restart_active=blocked forced_reboot_state=fresh-inactive upstream_gate=passed service_restart=running storage_recovery=none
```

This is a storage-recovery observation, not permission to auto-repair or
restore user data in production.

## Verification and handoff

- `make test`, `go test -race ./...`, `go vet ./...`, shell syntax, and
  whitespace checks pass.
- Two consecutive package builds produced the same seven-file FAT-safe archive,
  SHA-256 `44f8db3776c59c9396093d26ae6a3e2e6763498d67e6dedf3ca3af8db125f6cd`.
- `scripts/adb-mlp1-b0b-timing.sh` is destructive only inside its three exact
  fixture roots, uses real FAT-backed paths, a real inbound transfer, the real
  controller, and current Jawaka binaries.
- B-GAME-01 resolves to “pause unavailable; stop sole/default.”
- B-GAME-02 passes for the reachable stop-only restart states, with B1's
  transitive process-death matrix supplying the prerequisite crash proof.
- B-LIF-04 passes for active-game Run, immediate daemon recovery, and reboot
  reconciliation against the real controller.

B3 receives a stop-only lifecycle, the measured UI copy above, and no
first-release prelaunch pending-check setting.
