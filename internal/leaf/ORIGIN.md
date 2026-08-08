# Origin

This directory was copied from
`Utility-Muffin-Research-Kitchen/Leaf-Itchio-Pak` commit
`72cf35faca9f77cfca5bbb6dc0c7e037185fa276` on 2026-07-29.

All Go implementation and test files from the source `internal/leaf` directory
were copied unchanged for B0a. The module import in `storage_test.go` was
mechanically changed from `Leaf-Itchio-Pak/internal/leaf` to
`Leaf-Syncthing-Pak/internal/leaf`. B1 then changed only the app-owned durable
state directory name from `Itch-io` to `Syncthing`; shared Leaf path semantics
remain inherited from the recorded source.
