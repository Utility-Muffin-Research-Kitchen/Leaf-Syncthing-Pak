# B4b floor pak and catalog decision record

**Status:** Complete; review and merge pending

**Date:** 2026-08-09

**Candidate:** `agent/syncthing-floor-catalog`, based on `a1a647e`

**Device:** MLP1 ADB serial `f40098e329c73533`, qualified P2 platform
checkpoint running over a base `0.9.0` install

This record follows
`umrk-workspace/plans/leaf-syncthing/phases/phase-b4b-floor-and-catalog.md`.
Both physical cards were explicitly declared expendable. All catalog values in
this phase are disposable fixtures. No production metadata, catalog, tag, or
release asset was changed.

## Product result

B4b adds one inert compatibility-floor build input and one shared shell
version check. The floor displays the required and installed Leaf versions and
has no service declaration, controller, upstream Syncthing binary, network
path, or Syncthing data path. The real package uses the same check in both its
foreground launcher and new service wrapper. An incompatible or unreadable
installed version opens the floor from the launcher and makes the service
entry point fail closed; an unstamped development package remains runnable.

Production source stays unstamped. Packaging accepts `pak_version` and
`min_leaf_version` only as a pair and writes them into the assembled candidate,
not the source manifest. The floor builder requires its display minimum. B5
remains the sole owner of final version selection and every production stamp.

The local feed uses Leaf's existing `pakrat-local-feed.py` unchanged. It
combines the five existing production apps with Syncthing history whose legacy
fields point to ungated floor `0.0.1` and whose descending `versions[]` contains
real `0.0.2` gated at Leaf `99.99.99`, followed by the floor. There are zero
Pak Rat catalog-schema, generator-rule, gate-aware-client, or compatibility-
client changes.

## Artifact evidence

The disposable generator artifacts were deterministic across repeated builds:

| Artifact | Bytes | SHA-256 |
| --- | ---: | --- |
| Floor `0.0.1` | 36,735 | `fdf1d388873a4f0e656abc59fa6b030affd390b5e72d03594b74901b2b1a2ddb` |
| Real `0.0.2` | 14,178,504 | `912efb8310022352b19ee5ddef8485d34bf22e1f3d0642e9b7b26539114f5fa0` |

Both ZIP CRC audits pass. The floor's installed tree is 74,890 bytes and
contains only its C/Catastrophe executable, launcher, shared gate, runtime
manifest, minimum display input, and MIT notice. It contains neither
`min_leaf_version` nor a `service` object. The real candidate is 33,444,319
installed bytes and carries runtime `pak_version: 0.0.2`,
`min_leaf_version: 99.99.99`, and `service.sh`.

## Catalog and client evidence

`make b4b-local-smoke` passed all disposable selection cases:

- Leaf `0.9.0` selects floor `0.0.1` and explains that real `0.0.2` requires
  `99.99.99`;
- Leaf exactly `99.99.99` and a higher version select real `0.0.2`;
- install-target rechecks reject the incompatible real package;
- the real service wrapper below the minimum exits 64;
- all five baseline apps remain byte-for-byte ordered ahead of Syncthing.

The real pre-gating MLP1 client was built from Jawaka commit
`95de4829b1d0e494aadb4e5b5367d3d8f6a3a00c`, the parent of gate commit
`1ac5c77`. Against the combined feed it listed all six apps, selected only
floor `0.0.1`, downloaded it, and installed a tree with no service, upstream
binary, or runtime minimum. The test used a clean environment so the active
two-card `*_PATHS` values could not contaminate its isolated FAT fixture.

## MLP1 floor and two-card evidence

The floor UI ran as a real C/Catastrophe process on MLP1. It stayed alive for
inspection, opened no IPv4 or IPv6 socket, created no Syncthing path on either
SD card, then exited cleanly and returned control to the live launcher.

The actual-artifact two-card run used dedicated directories bind-mounted from
both real VFAT cards. It established Secondary ownership of floor `0.0.1`,
crossed the disposable minimum, and selected real `0.0.2`. The attempted
in-place update downloaded and validated the real candidate, then refused with:

```text
Syncthing is installed on Secondary. Uninstall it there first, then install the service pak on Primary.
```

At that boundary the Secondary floor and ownership record were unchanged,
Primary had no package, and exactly one Syncthing package id existed. Uninstall
then removed the Secondary tree and ownership row before the real version was
offered. The real candidate installed once on Primary, produced a matching
token-bearing record, remained disabled, and left no Secondary duplicate. Its
service wrapper subsequently refused a synthetic Leaf `0.9.0` runtime with
exit 64 before the fixture restored `99.99.99`.

## Verification disposition

| ID | B4b evidence |
| --- | --- |
| `B-CAT-01` | Real pre-gating client parses every app, selects and installs the floor; the on-device floor has no network or Syncthing writes. |
| `S-CAT-02` | Below/exact/above selection, install-time and runtime rechecks, artifact metadata agreement, Secondary refusal, committed uninstall, unique Primary install, and unchanged Pak Rat machinery all pass. |

Optional tests remain explicitly deferred by project decision. No optional
result is treated as a B4b or final-release prerequisite.

## Handoff

B5 receives the unstamped production inputs, shared runtime gate, disposable
artifact hashes, real pre-gating parse/install evidence, and actual two-card
transition evidence. B5 must choose the final Leaf version, stamp all required
destinations from one source, rerun release verification on exact final
artifacts, and is the only phase authorized to publish catalog data, tags, or
release assets.
