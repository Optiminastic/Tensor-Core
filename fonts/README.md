# Fonts the templates name

The plank templates set `FONT = "Segoe UI Black"`, and every `W_TBL` glyph-width
entry in them was measured from that face. Whatever is in this directory is
installed into the production-worker image and becomes what actually gets
printed.

**`seguibl.ttf` is committed, deliberately.** The worker image is built from
this repo by Coolify, so a font that is not in the repo is a font production
does not have — and a worker without it renders every plank thin while
reporting success.

That decision has a cost worth stating plainly: Segoe UI ships with Windows and
its licence does not contemplate redistribution inside a container image. This
was raised and accepted as a business decision, not overlooked. The
alternatives below remain available if it ever has to come back out.

**`bahnschrift.ttf` and `GOTHICB.TTF` are committed for the same reason.** The
Desk Name Plank (`desk-np`) sets its name in `Bahnschrift:style=Bold Condensed`
(a named instance of this variable font) and its role in `Century Gothic:style=Bold`,
and positions every letter from those faces' own width and kerning tables.
Without them the worker substituted a wider face and the letters overlapped
("ROHANMEHTA") while the render reported success. Both are Microsoft fonts,
the same licensing position as Segoe UI above.

## Why this directory has to exist at all

Without it, fontconfig does not fail — it **substitutes**, silently. In the
worker image `Segoe UI Black` and `DejaVu Sans` produce byte-identical output,
because the first resolves to the second:

| Font asked for | Present? | Lettering volume | vs intended |
| --- | --- | --- | --- |
| `Segoe UI Black` | no, substituted | 11,301.8 mm³ | −66.4% |
| `DejaVu Sans` | yes | 11,301.8 mm³ | identical — that is the substitution |
| `DejaVu Sans:style=Bold` | yes | 28,526.9 mm³ | −15.3% |
| `Segoe UI Black` | **yes, installed here** | **33,676.0 mm³** | **+0.000%** |

So a worker without the font renders planks with two thirds of the lettering
missing, and reports success. That is not a theory: it is the difference between
the thick planks on the floor and the thin ones a re-render produces.

## What to put here

`seguibl.ttf` — Segoe UI Black. It reports family `Segoe UI Black`, which is the
name the templates ask for.

If you are not licensed for it, the honest alternatives are:

- **`DejaVu Sans:style=Bold`** — already in the base image, needs no file here,
  and lands within 15%. A one-line change to `FONT` in the templates. Closest
  thing to free.
- **An OFL black-weight face** (Archivo Black, Montserrat Black) dropped in
  here, with `FONT` pointed at it **and `W_TBL` re-measured** — the table is
  glyph widths normalised to cap height, so a different face makes the existing
  numbers wrong and the auto-fit squeezes by the wrong amount.

Do not simply point `FONT` at a face whose widths differ without re-measuring:
the templates would then draw one font while sizing it as another, which is
exactly the state this directory exists to end.

**`arialbd.ttf` is committed for the same reason.** The Premium Dual Name
Plank's front plate (`internal/frontplate`, the shop's `front_plate.scad`) sets
its text in `Arial:style=Bold` and sizes it from Arial's own metrics
(`textmetrics`). Without it the worker would substitute another face and the
text would print in it while the build reported success. A Microsoft font, the
same licensing position as Segoe UI above.
