# Syncthing for Leaf v0.1.1

This update makes it easier to connect Leaf to an existing Syncthing hub and to
start over safely without entering long text on the handheld.

## Improved

- Hub-first setup now recommends showing Leaf's device ID or QR, adding Leaf on
  the computer, NAS, server, or VPS, sharing its existing Saves folder, then
  accepting the pending device on Leaf.
- The device screen explains that **Disconnected (Unused)** means the other
  device knows Leaf but has not shared a folder with it yet.
- Manual **Add peer by ID** remains available as a fallback, pending devices still
  require explicit acceptance and naming, and off-LAN **Sync Anywhere** remains an
  explicit confirmation.
- Recovery no longer asks you to type confirmation phrases. Reset actions show
  their consequences and exact deletion list as two controller confirmations.
- **Restore fresh setup** clearly returns Syncthing to a new local identity and
  configuration while preserving live Saves, States, ROMs, and card enrollment.

Includes Syncthing v2.1.3 and requires Leaf 0.10.0 or newer. Syncthing is
synchronization, not backup: deletions and corruption can propagate.
