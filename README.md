# Leaf Syncthing Pak

Optional Syncthing packaging and device integration for Leaf. Distribution is
through Pak Rat; this repository is not part of Leaf's default release payload.

Current work is the B0a upstream and platform qualification described in
`umrk-workspace/plans/leaf-syncthing/phases/phase-b0a-upstream-spike.md`.
The repository also contains the landed Go side of the shared CTL-1/LIFE-1
framed JSON transport and its canonical cross-language fixture tests. Local
tests expect the sibling `../umrk-workspace`; CI checks out the explicitly
pinned contract revision.

```sh
make verify-upstream
make test
make controller-mlp1
make package-platform PLATFORM=mlp1
```

With an MLP1 connected over ADB, run the controller smoke in an isolated
fixture with:

```sh
scripts/adb-mlp1-b1-controller-smoke.sh
scripts/adb-mlp1-b1-card-safety.sh
B1_MEASURE_SECONDS=600 scripts/adb-mlp1-b1-controller-smoke.sh
```

The command downloads only the locked upstream release inputs, rejects an
unexpected redirect host, verifies the release-key fingerprint and checksum
signature, verifies the archive digest, checks the annotated tag's peeled
commit, and inspects the archive layout without extracting it.

The current package is intentionally labeled `0.0.0-b1`: it contains the
verified upstream binary and the pure-Go resident controller with its LIFE-1
transport, persistent subscribe/state-reconciliation client, the first six
ordered controller startup steps (including recoverable
three-file config recovery, same-filesystem identity generation/promotion, and
disposable-copy migration that never replaces an existing certificate/key),
the token-preserving private-socket/LAN-only initial profile, and canonical
cross-language fixture tests. The package service runs the supervised upstream
lifecycle, strict PATH-2 card enrollment and inventory, fail-closed pre-B3
folder/conflict reconciliation, and the private UI control socket. The B0a
gateway spike remains source-only qualification evidence; the C/Catastrophe UI
and folder onboarding remain unfinished. Its foreground launcher exits with an
explicit development message while Jawaka owns the service entrypoint.
`Leaf-Syncthing-Pak` and
`build/mlp1/package/Syncthing.pak` are stable staging contracts that will
outlive this development artifact; the `0.0.0-b1` package itself must never enter a
Leaf release or production Pak Rat catalog.

Leaf can stage it only through the explicit optional-app path:

```sh
make -C ../Leaf stage-app APP=Leaf-Syncthing-Pak DEVICE=mlp1 \
  REMOTE_SDCARD_PATH=/the/intended/card
```

It remains excluded from Leaf's default stage, required bootstrap repos,
managed app manifest, and release ZIP.

An uncached targeted stage has a network precondition: `package-mlp1` downloads
the locked Syncthing release archive, signed checksums, source offer, and release
key. The verifier restricts initial and redirect hosts, validates the pinned
signer fingerprint and signatures, and checks every locked digest before the
artifact can be packaged.
Local tests expect `../umrk-workspace`; CI checks out the explicitly pinned
contract revision beside this repository. `make test` runs all Go packages and
the standalone C UI-protocol fixture check.

The UMRK controller and packaging code are MIT licensed. The bundled upstream
Syncthing binary remains under MPL-2.0; both notices ship in the pak.
