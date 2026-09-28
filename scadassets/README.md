# Template assets

The parts a `.scad` imports and the faces it names, staged beside every render
by `personalise.stageAssets`.

A template imports by relative name — `import("decoration.stl")`, `use
<fonts/Lobster-Regular.ttf>` — and OpenSCAD resolves those against the `.scad`'s
own directory. Every render gets a fresh temporary directory holding nothing but
the `.scad`, so without this the part is simply **absent** and OpenSCAD exits 0.

Measured on the heart keychain, rendering the `text` pass:

| staged | triangles |
| --- | --- |
| everything | 11,844 |
| no `decoration.stl` | 4,339 — the decoration silently vanished |
| no `Lobster-Regular.ttf` | 8,342 — fontconfig substituted another face |

Neither case errored. Both would have printed the wrong keychain while looking
like a successful render.

## What goes here

- **Top-level files** — the STL parts templates import. Copied as they are.
- **`fonts/`** — the faces templates name. Copied, and pointed at with
  `OPENSCAD_FONT_PATH`, because fontconfig substitutes a missing family rather
  than failing.

Any other subdirectory is ignored, so a folder of working files beside the parts
is not copied on every render.

## Licensing

Everything here ships in the production image, so it must be redistributable.

- `fonts/Lobster-Regular.ttf` — SIL Open Font License, `fonts/Lobster-OFL.txt`
  beside it. Free to embed.

`fonts/seguibl.ttf` in the repo root is a separate matter — see
`../fonts/README.md`.
