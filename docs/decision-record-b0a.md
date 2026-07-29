# B0a upstream and platform decision record

**Status:** Technical qualification complete; review/merge pending
**Date:** 2026-07-29
**Device:** MLP1 ADB serial `b1622a9e81b735ad`, Buildroot kernel 5.10.209,
Leaf release_id `0.7.0`

This record follows
`umrk-workspace/plans/leaf-syncthing/phases/phase-b0a-upstream-spike.md`.
Raw secrets, API keys, certificates, and peer identity material are excluded.

## Physical cards

| Card | Kernel device | Initial mount | Initial label | CID |
| --- | --- | --- | --- | --- |
| SD128, 128 GB | `/dev/mmcblk3p1` | `/mnt/sdcard` | `MLP ROMS` | `0353445344313238852b8a0b0c019b13` |
| SK64G, 64 GB | `/dev/mmcblk1p1` | `/media/sdcard1` | `MLP1FRESH0` | `035344534b36344786f40ca62a019bff` |

The user explicitly declared both cards expendable before destructive work.
Mount paths are observations, never card identity.

## Pinned upstream

| Field | Locked value |
| --- | --- |
| Version | `v2.1.2` |
| Annotated tag object | `9b63594106d94f5aa649beeea6f0161d4058f43a` |
| Peeled source commit | `44cbfcad56db30ccccdbbab40124fc498ee354db` |
| Binary asset | `syncthing-linux-arm64-v2.1.2.tar.gz` |
| Binary URL | `https://github.com/syncthing/syncthing/releases/download/v2.1.2/syncthing-linux-arm64-v2.1.2.tar.gz` |
| Binary SHA-256 | `2fcee9688f37df46337b0b78e7d2badc44549481e29eccaa8cdb1e698d79c8c5` |
| Signed checksum asset SHA-256 | `fc79100eb44eef13c0a4954afb73422bdc9efea866535b7525d5882d5e037b1a` |
| Signed checksum URL | `https://github.com/syncthing/syncthing/releases/download/v2.1.2/sha256sum.txt.asc` |
| Release-key URL | `https://syncthing.net/release-key.txt` |
| Valid release signer | `Syncthing Release Management <release@syncthing.net>` |
| Signing fingerprint | `FBA2E162F2F44657B38F0309E5665F9BD5970C47` |
| License | MPL-2.0 — `https://github.com/syncthing/syncthing/blob/v2.1.2/LICENSE` |
| Source offer | `syncthing-source-v2.1.2.tar.gz` (`584261eb7c705b9a2c4b624ba10bff8521de8c288bb7e2facffc7558291f678e`) — `https://github.com/syncthing/syncthing/releases/download/v2.1.2/syncthing-source-v2.1.2.tar.gz` |
| Source signature | `https://github.com/syncthing/syncthing/releases/download/v2.1.2/syncthing-source-v2.1.2.tar.gz.asc` |

`make verify-upstream` is the reproducible verifier. It accepts only the
pinned current-key `VALIDSIG`, not an arbitrary good signature, and rejects
download redirects outside the locked host set.

## Budgets fixed before measurement

| Quantity | Budget | Stop condition | Measured |
| --- | ---: | ---: | ---: |
| Idle RSS | 80 MB | 120 MB | 104,300 KiB end/max observed (acceptable; misses preferred budget) |
| Idle CPU, 10 minutes | 2% | 5% | 0.4008% (pass) |
| Idle SD writes, normalized hourly | 1 MB/h | 10 MB/h | target SD128: 0 B/h; service SD64: 266,718 B/h (pass) |
| Installed package size | 60 MB | 100 MB | 31,615,873 bytes (pass) |
| Gameplay p99 frame-time delta, paused | — | — | deliberately not required |
| Battery drain delta | — | — | deliberately not required |

The valid idle sample ran for 601.228 seconds with no browser forwarding or UI
polling. Receiver monitor/main processes consumed 241 scheduler ticks at
`CLK_TCK=100`; RSS moved from 104,060 KiB to 104,300 KiB. SD128 block-write
counters did not change. SD64 increased by 44,544 bytes, normalized to 266,718
bytes/hour. Per-process write bytes were unavailable because this kernel has
`CONFIG_TASKSTATS=n`, so the card block counters are the authoritative write
measurement. A prior sample interrupted by browser access was discarded and
is not included.

## D-4 — card removal boundary

### Fixture and invariant

The pinned `v2.1.2` arm64 binary ran as two isolated identities on the MLP1.
The receiver used a `receiveonly` folder at
`/mnt/sdcard/B0A/target`, custom marker `.leaf-card-b0a128`, and an external
trashcan versions path at `/mnt/sdcard/B0A/versions`. The sender lived on the
64 GB card, which remained inserted to preserve ADB control. Discovery, relay,
NAT traversal, usage reporting, and upgrades were disabled.

The unmounted `/mnt/sdcard` rootfs stub was made immutable with `chattr +i`.
Its measured invariant was:

- entry count: `9` (`find -xdev -print | wc -l`);
- `.leaf-b0a-stub-sentinel` SHA-256:
  `fb4d163e1c2d1df159e8348fdbbccd3ce66e31a4dea362c78c4e36d327e7b831`;
- `.leaf-b0a-stub-count/a` SHA-256:
  `b6a98d9ce9a2d9149288fa3df42d377c3e42737afdcdaf714e33c0a100b51060`;
- `.leaf-b0a-stub-count/b` SHA-256:
  `f2c82decdd7181cf98945929a62598db7e6b477e11f6e0eb0ae97020eff151ad`.

A real `/dev/mmcblk3p1` vfat mount succeeded over the immutable stub, and a
real unmount exposed the same immutable stub. Physical reinsertion also
mounted over it. Stock reinsertion used `noexec` even though the card's first
observed mount did not, so pak launchers must not assume executable SD mounts.

### Physical removal results

| Case | State at pull | State with card absent | Card/stub result | Verdict |
| --- | --- | --- | --- | --- |
| Scan | A 2 GiB direct target file was being scanned; scan began `2026-07-29T19:13:13+01:00` | `error`, `folder path missing`, one scan/pull error | No `B0A` path on rootfs; count, hashes, and immutable bit unchanged | Pass |
| Active receive | `syncing`, `240937472` bytes still outstanding | `error`, `folder path missing`, `536870912` needed bytes, one pull error | On reinsertion, a 512 MiB `.syncthing.*.tmp` and `98140160`-byte partial destination proved an in-flight write; resumed files matched SHA-256 `7292d64d70144169573d0ae49226bcce3f361fc9fd5c12fa655ebf0addf384ce`; stub unchanged | Pass |
| Rename | 5,000-file `rename-old` → `rename-new`; receiver had 2,668 old and 2,020 new files, with 3,085 changes pending | `folder path missing`, 2,864 files and 2,603 deletes still needed, 2,677 pull errors | Reinsertion preserved a mixed state (3,094 old, 2,211 new); stub unchanged | Pass with recovery caveat |
| Versioning | 5,000 replacements; 7,008 archived versions existed, 917 v2 targets existed, 4,107 updates remained | `folder path missing`, 3,832 files still needed, 3,832 pull errors | Reinsertion retained 7,086 versions but only 298 v2 targets, demonstrating lost recent FAT writes; stub unchanged | Pass with recovery caveat |
| Empty local index | Fresh paused folder had sequence/files/bytes all zero; card was removed before first scan, then the folder was unpaused | `error`, `folder path missing`, sequence/files/bytes remained zero | No `B0A-empty` path or marker created on rootfs; count, hashes, and immutable bit unchanged | Pass |

All four Syncthing monitor/main processes survived every removal. Every absent
card produced a visible folder error, and no tested operation wrote outside
the card. This satisfies the removal half of the binary D-4 threshold.

### FAT recovery observation

Unsafe removal during metadata-heavy operations produced kernel lost-write,
corrupt-directory, and read-only-remount diagnostics. A noninteractive
`fsck.vfat -a -v` after the rename pull:

- reclaimed 2,526 unconnected clusters (`82,771,968` bytes);
- cleared the dirty bit and corrected the free-cluster summary;
- renamed three pre-existing duplicate ROM directory entries to
  `FSCK0000.000` in their respective directories.

After repair, 660 stale rename-source files appeared as receive-only local
changes (`localFlags=8`). Syncthing correctly refused to delete their nonempty
directories. `POST /rest/db/revert?folder=b0a-card` cleared those local changes,
after which the receiver converged to 5,000 `rename-new` files, zero
`rename-old` files, zero pull errors, and the same relative manifest SHA-256 on
both cards:
`779f8729fa966d9e1051353db6016eff81d6b874d2c3737f1f8e951b50d9b2a8`.

This does not weaken the rootfs boundary, but B1/B3 must treat a physically
removed FAT card as potentially dirty and must expose a recovery-required
state. Automatic receive-only override is forbidden; it could propagate
recovered stale data to peers. Revert must be an explicit, bounded recovery
operation after filesystem repair and user confirmation.

### D-4 verdict so far

The production mechanism is implemented on
`miniloong-launcher-switcher` branch `agent/b0a-immutable-mount-stubs`. A
non-recursive bind view of `/` exposes the covered rootfs directories without
traversing either mounted card. The installed `/usr/bin/umrk-mount-stubs`
recursively locks only `/mnt/sdcard` and `/media/sdcard1` in that view with
`chattr +i`; `-xdev` bounds the traversal. The boot hook falls back to stock if
locking fails, and uninstall clears the attributes before removing the helper.

Direct MLP1 helper qualification passed:

- before production lock: primary immutable, secondary mutable;
- after lock: both immutable, and root writes to both underlays failed;
- both live mounted cards remained writable;
- unlock allowed controlled underlay writes;
- final lock restored both immutable attributes;
- the primary underlay remained 9 entries with all sentinel hashes unchanged;
- the secondary underlay remained one empty mountpoint directory.

The exact generated installer installed source-matching helper, boot-hook, and
session hashes. Its installed uninstaller removed those three files and made
both stubs mutable; reinstall restored both immutable attributes. Installer,
uninstaller, boot-hook, generator, runtime-env, unit, and shell-syntax checks
pass.

The production sources were committed as switcher commit
`f13980c4807ab977e85c4085da680209af0f977d` and installed again before a real
reboot. After ADB returned, the installed hook, session, uninstaller, and helper
SHA-256 values still exactly matched the committed sources. The helper reported
both stubs immutable. A bind view of the rebooted rootfs independently showed
both immutable attributes, the primary underlay's nine-entry count and three
sentinel hashes unchanged, and the secondary underlay as its one empty
mountpoint directory.

The reboot also swapped the physical cards' mountpoints: CID
`0353445344313238852b8a0b0c019b13` moved to `/media/sdcard1` and CID
`035344534b36344786f40ca62a019bff` moved to `/mnt/sdcard`. The
stub protection therefore persisted independently of card identity and mount
assignment. The previously stressed 64 GB FAT volume mounted read-only after a
kernel `fat_free_clusters: deleting FAT entry beyond EOF` error. Leaf was
stopped, only verified card-backed processes were terminated, the exact CID was
unmounted, and `fsck.vfat -a -v` reclaimed unconnected clusters and returned
zero. It then remounted read-write and passed a create/sync/remove check. This
was card recovery after the destructive fixture, not a rootfs-stub failure.

The default custom-marker plus immutable-stub design therefore passes all
physical removal, real mount/unmount, and install/removal cases. The private
mount-namespace contingency is not selected. Reboot persistence passes; no
stock OTA was staged because B0a has no authorized vendor firmware update
input.

## D-9/D-10 — browser gateway

An arm64 stdlib-only spike at `cmd/b0a-gateway-spike` terminated HTTPS on
`127.0.0.1:18443` and dialed the real pinned admin API through
`/tmp/leaf-syncthing-b0a/receiver/gui.sock`. It removes client `X-API-Key` and
`Authorization` headers, injects the private key read from a mode-0600 file,
strips upstream `Set-Cookie`, and rejects all upstream methods except `GET` and
`HEAD` with a Leaf-owned `405` response. `/leaf/status` is a Leaf-owned
read-only control-surface stub.

Measured results:

- a deliberately wrong client API key still produced a valid `200` read,
  proving strip-and-inject rather than client credential passthrough;
- `POST /rest/config` returned `405` and the Leaf read-only explanation;
- the upstream root returned `200`;
- all 47 script, stylesheet, font, and image assets referenced by the pinned
  root HTML returned `200` through HTTPS;
- health, system status, version, connections, folder status, and event reads
  all returned `200`;
- six consecutive event long-polls completed over 361 seconds, each after
  60.059–60.074 seconds, with valid monotonic JSON and no gateway failure;
- Syncthing itself returns `405` for `HEAD /rest/system/status`; the gateway
  permits and forwards HEAD, and its Leaf-owned endpoint answers HEAD, but it
  does not convert unsupported upstream HEAD requests into GETs.

The automated in-app browser refused the self-signed certificate with
`ERR_CERT_AUTHORITY_INVALID`, the expected first-contact D-10 warning, and its
security policy prohibited automation through the interstitial. The user then
performed the intended one-time manual acceptance in a normal browser and
captured the rendered page. It visibly showed pinned `v2.1.2, Linux (64-bit
ARM)`, both paused folders, the local `rk3566-buildroot` device, and
`B0A-Sender` as `Up to Date`. It also rendered the empty-index notices verbatim:
the root `mkdir` failed with `operation not permitted`, followed by `folder path
missing`.

**Verdict:** the upstream read-only proxy variant passes D-9. The upstream UI
does display action controls, but their requests terminate at the gateway's
tested Leaf-owned `405`; no upstream mutation is allow-listed. The smaller
Leaf-rendered fallback is therefore not selected for the first release.

The generated ECDSA certificate is persisted and used this fixed validity
window:

```text
notBefore=2020-01-01T00:00:00Z
notAfter=2120-01-01T00:00:00Z
```

The fixed dates make certificate generation independent of a stale MLP1 RTC;
certificate validation still correctly depends on the browser's clock. The
device measured `entropy_avail=256`, `/dev/rtc0` reported 2026-07-29, and the
private key and upstream API-key files were mode `0600`. The observed spike
certificate SHA-256 fingerprint was
`5E:43:D4:7F:23:04:11:CB:B9:51:F2:BB:DE:77:28:F7:67:44:4C:76:5E:76:EB:D2:49:27:49:DF:AA:C2:EF:D5`.

## D-14 — LAN-only enforcement

The receiver and sender used pinned `v2.1.2` TLS sync listeners at
`127.0.0.1:22001` and `127.0.0.1:22002`. The peer started connected with an
empty `allowedNetworks` list (the Sync Anywhere condition).

Changing the live peer configuration directly to the excluding
`192.0.2.0/24` range did **not** evict the established loopback connection
within 20 seconds. `allowedNetworks` is therefore a new-connection boundary,
not by itself a live-connection teardown mechanism.

The controller-qualified transition is:

1. pause the device and wait for the disconnected state;
2. apply the route-derived `allowedNetworks` list;
3. unpause the device;
4. verify connection state and kernel socket destinations.

With that sequence, pausing under `192.0.2.0/24` removed both established
kernel socket rows. Unpausing while the excluding range remained configured
stayed disconnected for the observation window, with zero direct
`22001`/`22002` established rows. Replacing it with `127.0.0.0/8` reconnected
and produced exactly these two `/proc/net/tcp` destinations:

```text
127.0.0.1:22002 -> 127.0.0.1:22001
127.0.0.1:22001 -> 127.0.0.1:22002
```

**Verdict:** D-14 stands, provided B2 implements the pause → policy update →
unpause transition and verifies the disconnect. A config-only update is
disqualified because it leaves a nonconforming live connection established.

## Unix socket and listener inventory

The pinned receiver and sender each ran the upstream monitor plus main process.
The GUI sockets were both `srw-------` (0600) on tmpfs. Receiver-owned socket
inodes were:

- one Unix listener at
  `/tmp/leaf-syncthing-b0a/receiver/gui.sock`;
- one loopback TCP sync listener at `127.0.0.1:22001`;
- loopback-only established sync connections;
- zero TCP GUI listeners and zero UDP/UDP6 sockets.

The sender equivalently owned its mode-0600 Unix GUI socket, loopback TCP sync
listener at `127.0.0.1:22002`, loopback-only established connections, and no
UDP/UDP6 sockets. The gateway alone owned the HTTPS listener at
`127.0.0.1:18443`. `/proc/<pid>/fd` socket inodes were correlated to
`/proc/net/{tcp,tcp6,udp,udp6,unix}`; this was not inferred from configuration.

**Verdict:** the pinned binary's private Unix admin socket works on this kernel
with explicit 0600 permissions. There is no fallback TCP admin listener.

## Reused Leaf integration

`internal/leaf` was copied from `Leaf-Itchio-Pak` commit
`72cf35faca9f77cfca5bbb6dc0c7e037185fa276`; the only mechanical adaptation is
the copied external-test module import. The generic Catastrophe bridge and
composer subset (`cat_bridge.c/.h`, `catui.go`, `primitives.go` and its test)
was copied byte-for-byte from the same commit. App-specific Itch.io screens
were not copied because they import unrelated `appui`, `logger`, and `media`
packages. Each directory contains `ORIGIN.md`.

Native Catui and Leaf tests pass. The Catui cgo test binary also built with the
actual `mlp1-toolchain` Buildroot flags and Go 1.22.12 image as AArch64 ELF,
kernel baseline 5.10, interpreter `/lib/ld-linux-aarch64.so.1`, SHA-256
`636916df443a54ede2291a025f674ff8ff31ad3468e08171e6f2e335b86f39bb`.

## Leaf staging isolation

Leaf's explicit dispatcher succeeded with:

```sh
make stage-app APP=Leaf-Syncthing-Pak DEVICE=mlp1 \
  REMOTE_SDCARD_PATH=/media/sdcard1
```

The target was intentionally explicit because both cards were mounted. The
original qualification deployed six files and 31,615,874 bytes to
`/media/sdcard1/Apps/mlp1/Syncthing.pak`; its hashes matched the then-current
working tree. Publishing normalized one trailing newline in the packaged lock
file, making the reviewable package one byte smaller without changing either
binary. A later mount assignment put the same marked 64 GB card at
`/mnt/sdcard`; an exact post-publication targeted-stage rerun deployed
31,615,873 bytes and produced combined host/device manifest SHA-256
`7893a53488abb0da51a408805ec7176a33c75a53506afdd18fa553ba98cbd971`.

On Leaf branch `agent/b0a-syncthing-targeted-stage`, the app is classified as
Pak Rat-owned for explicit `stage-app` use. The strengthened policy smoke test
passes and rejects `Syncthing.pak` from default `STAGE_APPS`, required
bootstrap repos, `managed-apps.txt`, the platform manifest, and release `Apps`
content. It is absent from both production `STAGE_APPS` definitions.

## Verdicts and handoff

- D-4 keeps the custom marker plus immutable rootfs mount stubs; no private
  mount namespace is required by the measured device behavior.
- D-9 selects the read-only upstream proxy presentation.
- D-14 requires pause → policy update → unpause plus socket verification; a
  config-only update is insufficient.
- The pinned artifact, required short-run budgets, targeted staging isolation,
  and physical removal/recovery gates pass.
- Repeated gameplay-frame and multi-hour battery baselines were explicitly
  removed as phase and release requirements on 2026-07-29. They may be run
  later but do not block B1, Release P, or Release B.

B1–B3 must implement the selected mechanisms and B0b must still prove the
bounded lifecycle pause/stop semantics against the production controller.
