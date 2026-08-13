# Updating the pinned Syncthing release

Leaf ships one verified upstream Syncthing binary inside the optional Pak. Do not
change the pin by editing a version string alone.

## Prepare the new lock

1. Start from a stable release on the official
   [Syncthing releases page](https://github.com/syncthing/syncthing/releases).
2. Record the annotated tag object and peeled commit with:

   ```sh
   git ls-remote --tags https://github.com/syncthing/syncthing.git \
     refs/tags/vX.Y.Z 'refs/tags/vX.Y.Z^{}'
   ```

3. Record the release timestamp, ARM64 archive name, byte size, SHA-256, signed
   checksum-file SHA-256, source archive SHA-256, and source-signature SHA-256.
   Use the immutable release assets, not a branch archive.
4. Keep the pinned release-key fingerprint and allowed download hosts unless the
   upstream project has deliberately rotated them. Treat a key change as a security
   review, not a routine bump.
5. Add `upstream/syncthing-vX.Y.Z.lock.json`, update `release-lock.json`, the
   package builder, `PinnedUpstreamVersion`, and the gateway's documented UI pin.
   Remove the superseded lock only in the same reviewed change.

## Verify the inputs and package

Run from the repository root:

```sh
make verify-upstream
make test
go vet ./...
go test -race ./internal/controller ./internal/syncthing ./internal/gateway
make package-mlp1
python3 scripts/release-package-check.py \
  --kind real --archive build/mlp1/Syncthing.mlp1.pak.zip
```

`verify-upstream` restricts initial and redirect hosts, checks every locked digest,
verifies both the signed checksum list and detached source signature against the
pinned release-key fingerprint, checks the signed entries for the ARM64 and source
archives, verifies the Git tag object and commit, and audits the binary archive
shape.

## Device requalification

An upstream bump must repeat the Syncthing blocker checks plus these focused checks:

- Start and stop cleanly, then force the group-wide TERM/KILL fallback.
- Record the device ID and SHA-256 of `cert.pem` and `key.pem`; update the Pak,
  restart, rebuild only the derived index, and confirm all three values are unchanged.
- Open an existing v2 database and configuration, sync in both directions, exercise
  a conflict and version recovery, and confirm all configured folder IDs remain.
- Run create, join, later-peer, second-Leaf, card-swap, first-sync, reset, uninstall/
  reinstall, and game check-then-stop paths against the new binary.
- Pair the real read-only gateway, load the pinned upstream dashboard, and confirm
  known mutation controls are hidden while every mutation is still rejected by the
  gateway policy.
- Repeat the idle memory measurement for the controller and upstream process.

Do not automatically downgrade across an upstream database incompatibility. Preserve
configuration and certificate identity; when compatibility permits, remove and
rebuild only the derived index after explicit confirmation.

## Publish and notify

Update the Pak version and immutable catalog history entry, build from the reviewed
commit, and let the tag workflow create a draft release. Verify the uploaded asset's
size and SHA-256 before publishing it. Never replace an older release asset or catalog
history entry.

The installed upstream version is visible on the Syncthing app's main screen and in
its redacted diagnostics. Release notes should name both the Pak version and upstream
Syncthing version so users can tell whether an update is available.
