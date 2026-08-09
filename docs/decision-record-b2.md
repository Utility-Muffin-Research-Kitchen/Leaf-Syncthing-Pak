# B2 device UI and gateway qualification record

**Status:** Complete; implementation and required B2 host/device qualification
passed, review/merge pending

**Date:** 2026-08-09

**Device:** MLP1 ADB serial `f40098e329c73533`; two enrolled, user-declared
expendable cards

**Leaf staging commit:** `9cff2d8a5d3e186a8b5e33ae79f458d10ff909cc`

**Jawaka commit:** `f50d5ce745fdbcc7a65551d3783558d2ff1d0a7c`

**Contract pin:** `f7ffb06eaf266cab4404d6e75dfe0e18956d02b8`

**B2 implementation commits:** `66068a0`, `f217903`, `c8c6b13`, `e7e1f04`,
`389f2a8`, `a33ce16`, and `3d703e5`

No API key, certificate, private key, pairing material, full peer id, or trust
cookie is recorded here.

## Delivered boundary

B2 keeps D-17's production split. `leaf-syncthing` is a statically linked,
`CGO_ENABLED=0` ARM64 controller and gateway. `leaf-syncthing-ui` is a separate
ARM64 C/Catastrophe/SDL executable. The UI uses CTL-1 for generic service
actions and the package-private framed v1 socket for Syncthing-specific reads
and mutations. It contains no Go runtime or cgo path.

The device UI covers service state and enablement, transfers, enrolled and
absent cards, folders and conflicts, peers, LAN-only/Sync Anywhere, gateway
pairing and trust, bounded logging and diagnostics, snapshot/version inventory,
and the three reset choices. B3 still owns guided folder onboarding and release
of the durable `first-sync` pause.

An ARM64 `strace` run of the packaged UI used SDL's headless driver and reached
the real running controller. Its only Unix connections were `jawakad.sock` and
`services/org.umrk.syncthing/control.sock`; it never opened the upstream
`syncthing-gui.sock`, Syncthing config/data, or controller-owned durable state.
Foreground UI RSS was 12,588 KiB. The same binary initialized Catastrophe,
fonts, input, renderer, and the real overview without error.

## Network profile

The final generated LAN-only configuration was inspected through the private
upstream socket:

| Setting | Final value |
| --- | --- |
| Local discovery | enabled |
| Direct encrypted listener | enabled (`:::22000`) |
| Global discovery | disabled |
| Relays | disabled |
| NAT traversal | disabled |
| Usage reporting | disabled (`urAccepted=-1`) |
| Admin GUI | Unix socket only, mode `0600`; no TCP `8384` listener |

The live socket matrix passed:

- a real peer on the same LAN connected over a globally scoped IPv6 address;
- a reachable RFC1918 test peer behind an excluded route remained offline in
  LAN-only, connected after Sync Anywhere cleared `allowedNetworks`, and was
  disconnected when LAN-only was restored;
- adding `10.123.45.0/24` as a temporary directly connected `wlan0` prefix made
  that peer eligible in LAN-only; removing the prefix recomputed the allow-list
  within the controller's polling interval, closed the established TCP socket,
  and closed an open gateway listener;
- Sync Anywhere off → on → off restored the exact route-derived LAN profile.

The target kernel did not provide a usable tun/dummy-interface creation path,
so the negative case used a controlled reachable excluded route. Linux route
fixtures separately prove that `tun`, `tap`, `wg`, `ppp`, and other virtual
interfaces are excluded. The positive IPv6 and physical-prefix transition were
both exercised on the real `wlan0` path.

## Read-only HTTPS gateway

`ADB_SERIAL=f40098e329c73533 scripts/adb-mlp1-b2-gateway-smoke.sh` passed against
the staged production controller. It exercised the QR-fragment flow, exact
Origin and CSRF submission, host-only cookie attributes, pinned UI load,
event long-poll, status/config reads, recursive configuration-secret redaction,
unknown-path and mutation denial, displayed TLS fingerprint, logout revocation,
and listener close.

Host tests exercise every numeric GATE-1 rule: 120-second PIN/token lifetime,
expired PIN and QR-token rejection, PIN and token replay rejection,
replacement-offer invalidation, five failures
per source per 30 seconds, twenty failures per ten minutes with device lockout,
single-use consumption before trust, fixed 15-minute extension, 32-record hash-
only trust storage, and idle plus absolute expiry. Adversarial tests reject
request bodies, method-override headers, CONNECT, oversized pairing input,
arbitrary/open-proxy targets, unknown redirects, client credentials, upstream
cookies, and CORS.

Network/prefix changes, profile changes, CTL-1 service stop, package quiesce,
revoke-all, logout, controller shutdown, foreground exit, and extension expiry
all reach the controller-owned close path. Suspend and uninstall consume the
same already-qualified Jawaka stop/package-quiesce boundary; B4a still owns the
uninstall disclosure and transaction.

## Settings, diagnostics, and reset

On device, debug logging received a fixed 15-minute expiry, persisted it across
a real Jawaka stop/run cycle, and returned to normal on request. The fixed-path
diagnostics export succeeded and was scanned against the live API key and both
known full device ids without finding credentials, tokens, cookies, PINs,
private-key fields, peer identities, or folder paths.

Snapshot/version inventory loaded two rows per card with bounded names and byte
totals. The absent-card reset path was exercised by moving the Secondary vfat
mount to a temporary holding mount while the service was stopped. The restarted
controller showed the enrolled card as absent, refused full reset with
`card-absent`, and produced a separately confirmed available-only plan naming
the exact Secondary snapshot/version roots it would retain. The mount was then
moved back without unmounting or changing either card.

The first physical full-reset attempt exposed that the helper had queried
CTL-1 `list`, whose rows do not carry ownership/lease fields. It failed before
durable intent and changed nothing. Commit `3d703e5` switched the proof to the
per-service CTL-1 `status` response and added safe-owned/held/missing-field
regressions.

The corrected full reset then:

1. sealed eleven exact roots across primary controller state and both cards;
2. stopped the service and proved no pgid owner or generation lease;
3. persisted and executed the reset intent;
4. removed configuration/identity, derived data, backups, browser trust and
   certificate, folder-control state, and both cards' snapshot/version roots;
5. preserved byte-identical sentinels under Saves, States, and Roms on both
   cards;
6. cleared plan and intent last, restarted, generated a different Syncthing
   identity, and retained both card enrollments.

Host fault injection covers every reset-intent boundary. Before intent, all
prior durable Syncthing state remains; after intent, recovery completes the
same exact deletion set. Required-card mismatch, symlink roots, live-content
paths, stale plans, and interrupted recovery all fail closed.

## Resource and artifact gates

The first ten-minute run found avoidable work: a closed gateway still enumerated
interfaces every 500 ms, producing 2.6113% controller CPU of one core. Commit
`3d703e5` makes a closed gateway tick return before address discovery. The
clean-reset rerun sampled 119 times over 602 seconds:

| Process set | Average RSS | Maximum RSS | CPU (% of one core) |
| --- | ---: | ---: | ---: |
| Controller | 12,580 KiB | 12,756 KiB | 1.2691% |
| Upstream monitor + main | 49,524 KiB | 51,916 KiB | 0.2641% |
| Combined | — | **64,248 KiB (62.7 MiB)** | **1.5332%** |

The result is below the preferred 80 MiB RSS and 2% one-core CPU budgets and
well below the 120 MiB/5% stop conditions. On the four-core MLP1, combined idle
CPU is about 0.383% of total CPU capacity. No growth trend or service restart
occurred. Foreground UI memory is reported separately above.

The deterministic nine-file pak is 33,223,000 installed bytes. Two consecutive
builds produced the same ZIP SHA-256:
`0dac29e0040a664166dadeb8c9c28d82b76993f17768ca167dc9716e162525ac`.
Every archive path is a regular FAT-safe member below `Syncthing.pak/`. The pak
contains the UMRK MIT notice, qrcodegen MIT notice, Syncthing MPL-2.0 notice,
and signed upstream lock evidence.

## Verification disposition

| ID | Result |
| --- | --- |
| `B-NET-01` | Pass: real global IPv6, controlled excluded-route RFC1918, physical-prefix recompute, and socket close |
| `B-SYN-07` | Pass: cgo-disabled controller, separate C UI, C/Go fixtures, syscall trace, and 10-minute resource gate |
| `B-REC-01` reset branch | Pass: all host fault boundaries plus corrected physical two-card reset |
| `B-GATE-01` | Pass: fixed GET/HEAD allow-list, config redaction, secret scan, and mutation denial |
| `B-GATE-02` | Pass: real QR pairing plus all numeric lifetime/replay/rate-limit rules |
| `S-NET-02` | Pass: exact default and off/on/off profile |
| `S-UI-01` | Pass: C state surfaces, CTL-1 generic actions, private-socket-specific actions, and device trace |
| `S-GATE-02` | Pass: real pinned UI, reads, and event long-poll |
| `S-GATE-03` | Pass using device route/profile/service/revoke/exit tests plus fixed-lifetime host tests |
| `O-GATE-04` | Pass: method, body, override, redirect, query, and open-proxy adversarial sweep |

`go test ./...`, `go test -race ./...`, `go vet ./...`, both standalone C
protocol suites, shell syntax, upstream signature/digest verification, package
build, archive audit, explicit Leaf staging, and the real-device smoke all pass.

## Handoff

B2 introduces no new contract and has no remaining implementation item. B3 is
next: guided Saves/States folder onboarding, durable first-sync protection and
release, and stop-only gameplay lifecycle integration. The `0.0.0-b2` pak
remains a non-production targeted-development artifact and must not enter a
Leaf release or Pak Rat catalog.
