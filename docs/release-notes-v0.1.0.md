# Syncthing for Leaf v0.1.0

The first tester release keeps game saves and optional save states synchronized
between Leaf, computers, servers, NAS devices, Android handhelds, and other Leaf
devices.

## Included

- Guided create-or-join setup with explicit devices and per-card Saves/States
  bindings.
- First-sync safety copies, local version history, conflicts, and recovery tools.
- Automatic physical-card tracking when mount points swap after a reboot.
- Safe game launch and resume: pending files are shown, Syncthing is verified
  stopped before an emulator writes, and it resumes after play.
- LAN-only and Sync Anywhere profiles plus a paired, read-only browser status view.
- Syncthing v2.1.3, with its signed upstream release inputs recorded in the Pak.

Requires Leaf 0.10.0 or newer. Syncthing is synchronization, not backup: deletions
and corruption can propagate. Abrupt power loss during upstream index/version writes
and unusual FAT32 names, case collisions, timestamps, or files over 4 GiB have not
received the optional extended qualification sweep.
