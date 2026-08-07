# Leaf Syncthing

Optional Syncthing controller and UI for Leaf. This repository is being built
against the contracts in the sibling `umrk-workspace` repository.

The current bootstrap contains only the Go side of the shared CTL-1/LIFE-1
framed JSON transport and its canonical cross-language fixture tests. It is not
yet an installable `.pak` and does not start Syncthing.

Run the current checks from this directory with:

```sh
make test
```

Local tests expect `../umrk-workspace`; CI checks out the explicitly pinned
contract revision beside this repository.
