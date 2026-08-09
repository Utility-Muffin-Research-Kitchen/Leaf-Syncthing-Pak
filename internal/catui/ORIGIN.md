# Origin

The files in this directory were copied from
`Utility-Muffin-Research-Kitchen/Leaf-Itchio-Pak` commit
`72cf35faca9f77cfca5bbb6dc0c7e037185fa276` on 2026-07-29.

Copied unchanged: `cat_bridge.c`, `cat_bridge.h`, `catui.go`, `primitives.go`,
and `primitives_test.go`. These are the generic Catastrophe cgo bridge and
layout/composer primitives. Itch.io-specific screens were intentionally not
copied because they import that app's `appui`, `logger`, and `media` packages.
