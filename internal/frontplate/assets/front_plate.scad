// =============================================================
//  Premium DNP - front text plate  (replica of order 115183 "I LOVE YOU ♥")
//  Plate 192.8 x 14.6 x 1.0 mm, corner radius 5.4 mm.
//  Text: Arial Bold, capitals 10 mm tall, letters 0.4 mm thick inlaid into
//  the top face, centred on the plate (on the visible outline of the text,
//  both directions). Emoji / symbols are allowed in the text.
//
//  Needs OpenSCAD 2025+ with Preferences > Features: "textmetrics" and
//  "lazy-union" ticked (lazy-union = plate and text as 2 parts in the 3MF).
// =============================================================

// ---------- CUSTOMISE HERE ----------
plate_text = "I LOVE YOU ♥";

// what to render / export:  "all" (2 parts), "plate", "text"
part = "all";

// false = text on top (like the original).  true = text face on the bed
// (mirrored, prints the text glossy-smooth on the build plate).
text_face_down = false;

// ---------- STYLE (measured from the original, normally leave alone) ----------
font_name      = "Arial:style=Bold";
emoji_font     = "Segoe UI Emoji";
cap_height     = 10.0;    // height of capital letters, mm
emoji_height   = 10.0;    // height of a typical emoji, mm
max_text_width = 182.0;   // longer text is scaled down to fit
max_text_height = 12.6;   // taller text (descenders + big emoji) is scaled down to keep 1 mm margin
// symbols Arial itself has (drawn in Arial like the original heart)
arial_symbols  = "♥♠♣♦☺☻♪♫☼♀♂•…–—‘’“”€£₹™©®°";

plate_length    = 192.8;
plate_width     = 14.6;
plate_thickness = 1.0;
corner_radius   = 5.4;
text_depth      = 0.4;    // letter thickness (inlay depth), mm

plate_color = "white";
text_color  = "red";

$fn = 48;

// ---------- TEXT LAYOUT ----------
// heart emoji are drawn as the solid Arial ♥ (the emoji font's ❤ is hatched and prints badly)
heart_emoji = "❤♡💖💕💗💓💞💘💝💟🧡💛💚💙💜🖤🤍🤎";
// characters that only modify the previous emoji are dropped (variation selector, ZWJ)
function clean(s) = [for (c = s) if (ord(c) != 65039 && ord(c) != 8205 && ord(c) != 65038)
                         len(search(c, heart_emoji)) > 0 ? "♥" : c];
function is_arial(c) = ord(c) < 8192 || len(search(c, arial_symbols)) > 0;

// split into runs of the same font: [[string, is_arial], ...]
function join(cs, a, b) = a > b ? "" : str(cs[a], join(cs, a + 1, b));
function run_end(cs, i, f) = (i + 1 < len(cs) && is_arial(cs[i + 1]) == f) ? run_end(cs, i + 1, f) : i;
function runs(cs, i = 0) = i >= len(cs) ? [] :
    let(f = is_arial(cs[i]), e = run_end(cs, i, f)) concat([[join(cs, i, e), f]], runs(cs, e + 1));

// textmetrics() only works when it is ticked in Preferences > Features.
// Without it the text is still centred, but emoji can't be mixed in.
H_REF = textmetrics(text = "H", size = 10, font = font_name);
TM_OK = !is_undef(H_REF);

// font size that gives the wanted cap / emoji height (OpenSCAD sizes are not mm;
// Arial capitals are 0.716 em and 1 em = size / 0.72)
arial_size = TM_OK ? 10 * cap_height / H_REF.size[1] : cap_height / 0.716 * 0.72;
emoji_ref  = TM_OK ? textmetrics(text = "😀", size = 10, font = emoji_font) : undef;
emoji_size = TM_OK ? 10 * emoji_height / emoji_ref.size[1] : arial_size;
// emoji are shifted so a typical one is centred on the capitals
emoji_dy   = TM_OK ? cap_height / 2 - (emoji_ref.position[1] + emoji_ref.size[1] / 2) * emoji_size / 10 : 0;

function rfont(r) = r[1] ? font_name : emoji_font;
function rsize(r) = r[1] ? arial_size : emoji_size;
function rdy(r)   = r[1] ? 0 : emoji_dy;
function tm(r)    = textmetrics(text = r[0], size = rsize(r), font = rfont(r));

CLEAN = clean(plate_text);
RUNS  = TM_OK ? runs(CLEAN) : [];
TMS   = [for (r = RUNS) tm(r)];
// x start of each run (sum of previous advances)
X0   = [for (i = [0 : len(RUNS)]) i == 0 ? 0 : X0_sum(i)];
function X0_sum(i) = i == 0 ? 0 : X0_sum(i - 1) + TMS[i - 1].advance[0];
// ink extent of the whole line (what you actually see), used to centre it on the plate
HAS_RUNS = len(RUNS) > 0;
INK_L = HAS_RUNS ? min([for (i = [0 : len(RUNS) - 1]) X0[i] + TMS[i].position[0]]) : 0;
INK_R = HAS_RUNS ? max([for (i = [0 : len(RUNS) - 1]) X0[i] + TMS[i].position[0] + TMS[i].size[0]]) : 0;
INK_B = HAS_RUNS ? min([for (i = [0 : len(RUNS) - 1]) rdy(RUNS[i]) + TMS[i].position[1]]) : 0;
INK_T = HAS_RUNS ? max([for (i = [0 : len(RUNS) - 1]) rdy(RUNS[i]) + TMS[i].position[1] + TMS[i].size[1]]) : 0;
FIT   = HAS_RUNS ? min(1, max_text_width / (INK_R - INK_L), max_text_height / (INK_T - INK_B)) : 1;

module text_2d() {
    if (HAS_RUNS)
        scale(FIT) translate([-(INK_L + INK_R) / 2, -(INK_B + INK_T) / 2])
            for (i = [0 : len(RUNS) - 1])
                translate([X0[i], rdy(RUNS[i])])
                    text(RUNS[i][0], size = rsize(RUNS[i]), font = rfont(RUNS[i]), halign = "left", valign = "baseline");
    else if (len(CLEAN) > 0)
        // no textmetrics: let OpenSCAD centre the whole line itself
        text(join(CLEAN, 0, len(CLEAN) - 1), size = arial_size, font = font_name, halign = "center", valign = "center");
}

// ---------- MODEL ----------
module plate_2d() offset(r = corner_radius) square([plate_length - 2 * corner_radius, plate_width - 2 * corner_radius], center = true);

module text_3d(extra = 0)
    translate([0, 0, plate_thickness - text_depth])
        linear_extrude(text_depth + extra) intersection() { plate_2d(); union() text_2d(); }

module plate_3d() difference() { linear_extrude(plate_thickness) plate_2d(); text_3d(0.01); }

module orient() {
    if (text_face_down) translate([0, 0, plate_thickness]) rotate([180, 0, 0]) children();
    else children();
}

if (part == "all" || part == "plate") color(plate_color) orient() plate_3d();
if (part == "all" || part == "text")  color(text_color)  orient() text_3d();

if (TM_OK)
    echo(str("Text width: ", round((INK_R - INK_L) * FIT * 10) / 10, " mm", FIT < 1 ? str("  (scaled to ", round(FIT * 100), "% to fit)") : ""));
else
    echo("NOTE: tick Edit > Preferences > Features > textmetrics (and lazy-union) for emoji, auto-fit of long text and 2-part export.");
