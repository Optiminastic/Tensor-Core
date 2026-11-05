# OpenSCAD templates

These are **copies** of the shop's master files, which live outside the repo at:

    C:\Users\optiminastic\Desktop\ALL MODELS FILES\DNP\copy\

They are embedded (`//go:embed templates/*.scad`) rather than read from disk so
the service is self-contained: no shared drive to mount, no path to configure,
and the template that rendered a plank is the one committed alongside the code
that drove it.

**The cost of that is drift.** Editing the master does not change what Tensor
renders. It has already happened once: these copies sat five days behind the
masters, and the gap was only found by comparing file sizes. When you re-copy,
render the same names from both versions and compare the vertex SETS rather than
the files - OpenSCAD emits facets in a different order between runs, so identical
geometry produces different bytes and a byte comparison cries wolf.

When a master changes, copy it here and run the render tests - they
measure the output geometry, so a template that stops producing a 200x50x40
plank fails rather than quietly shipping the wrong size.

## The three files

They are not variants of one template with different defaults; the differences
are structural and cannot be layered on with `-D`:

| File | Hearts | Margins L/R | Plate |
|---|---|---|---|
| `dnp_two_heart.scad` | 2 | 60 / 60 | 1.8 mm |
| `dual_one_heart.scad` | 1 | 12 / 50 | 1.8 mm |
| `dnp_with_no_heart.scad` | 0 | 12 / 12 | 1.8 mm |

**One exception:** the two-heart plank takes 70/70 when either name runs past
seven letters, set from Go (`needsWideMargins`). Two hearts already consume
padding slots, so a long name on top of them runs the glyphs into the plate
edge. Below the threshold the 60/60 here stands.

**Otherwise margins live here, not in Go.** They were briefly passed as `-D` overrides,
which meant editing a master quietly changed nothing - a worse failure than a
wrong number, because the file said one thing and the printer did another.

## Output size

Each file ends with an `OUT_X` / `OUT_Y` / `OUT_Z` block that scales the
finished model to an exact bounding box. That is the step that used to be done
by hand in Bambu Studio with **uniform scale** unticked.

It is applied as `scale()`, not `resize()`, and the masters explain why:
`resize()` would fit whatever it is given into the target box, so exporting
`PART="text"` alone would blow the letters up to the full product size.

The geometry grows with the text - "SUBHANJANA" / "SUBHANTIKA" measures about
427 mm before scaling - so omitting these produces a model that looks right and
is more than twice the size it should be.

## The letters must not punch through the plate

`SINK` is how deep the glyphs sit INTO the plate. With `PLATE_T` at 1.8 mm and
`SINK` at 2.0 mm the letters went straight through: the plate floated at
z = +0.2 while the letters still started at z = 0, so they hung below the base
and printed as outlines in the first layer instead of a solid plate.

The masters now clamp it - `SINK_EFF` keeps a `FLOOR_MIN` of 0.8 mm of solid
plate under every letter - and echo a note when they do. That clamp is also
what makes the finished height come out at exactly 40 mm rather than 40.32.

## The scale is non-uniform, and that is deliberate

Forcing a plank that naturally measures 317 mm into 200 mm scales X by 0.63
while stretching Z by 1.61, which thins every stroke: a stroke-to-cap ratio of
0.118 against the 0.302 measured off `DNP.f3d`. The letters look visibly more
spindly the longer the name.

This was measured, discussed and accepted: exact product dimensions matter more
than letter weight. The alternatives - letting the plank grow (correct letters,
a different length per order) and a uniform scale into 200 mm (correct letters,
but 32 x 16 instead of 50 x 40) - were both rejected. Do not change it without
asking.

## A file can say how its variables are filled

Everything above is about the three DNP masters, which Go drives by name. A
**registry product** is driven by a mapping instead: a row per variable saying
which of the customer's answers feeds it.

Somebody types that mapping, once per design, and this shop's labels make that
worse than it sounds. The same first name is called `STEP 4-First Name-`,
`first name on plank`, `name-1`, `left name` and `top name` across five live
products, so there is no convention to read off and every new design means
sitting down with a list of its own orders.

**The person who draws the file already knows.** They named the variable and
they built the storefront field. So they can say so, in the comment the
customizer already shows:

```openscad
/* [Order] */
NAME_L = "";    // The left name. @from STEP 4-First Name- @max 20
NAME_R = "";    // @from STEP 5-Second Name @optional
HEARTS = 1;     // @from RED HEART @count @default 0
OUT_X  = 200;   // @fixed
PART   = "all";
```

| Directive | Means |
|---|---|
| `@from <label>` | The storefront field that fills it, worded exactly as the storefront words it. The value runs to the next `@`, so a label keeps its spaces. |
| `@fixed [value]` | Not the customer's. Bare, it means the value already written in the file - which is how `OUT_X = 200` is stated once rather than twice. |
| `@optional` | An order may leave it blank. The default is required, which **holds the order**. |
| `@default <value>` | What an unanswered optional field passes instead of the file's own default. |
| `@max <n>` | How many characters the text box fits, 1-120. The personalisation link enforces it in the form rather than in a render half a minute later. |
| `@text` `@number` `@count` | How the answer is read. Usually inferred from the declared value; `@count` reads a number out of wording, because the storefront sells "1 RED HEART", not "1". |

Three things to know:

- **Prose stays prose.** A comment with no `@` is documentation, exactly as
  before, and so is an `@` word that is not in that table. Every existing
  master is unaffected - `name1 = "DHAKSRAM"; // max 10 letters, A-Z` is read
  as the sentence it is.
- **It is a SUGGESTION, never the live mapping.** Uploading the file does not
  configure anything. Design & fields offers "Suggest mapping", fills the form
  from this, and somebody presses Save. A file that re-pointed a live
  product's fields on upload would show up first as a batch of planks with the
  names swapped.
- **Only top-level declarations count**, the customizer's own rule. A variable
  assigned inside a `module` cannot be set with `-D` at all, so offering it
  would be offering something that silently does nothing.

Without any of this, a product can still be mapped: Tensor pairs the file's
variable names against the labels on that product's own recent orders and
proposes what matches. Annotating is what makes a design mappable **before**
it has sold anything.
