// =====================================================================
//  DNP CHARM + LEAF JOINERY - DNP with universal snap-in pegs for the charms
//  Charm space: 40 mm on the RIGHT (left space 8 mm). The pegs are universal:
//  every charm gets the same holes, so any charm fits any plank, any time.
//  Whole-name method, same as Fusion:
//    1. each name is typed as ONE text line (Segoe UI Black, 25 mm, spacing 7)
//    2. name 1 is extruded through, name 2 is extruded through at 90 deg
//    3. the two whole-name solids are intersected (gives a grid of blocks)
//    4. only the blocks that are "letter i of name 1 x letter i of name 2"
//       are kept - the same blocks you pick by hand in Fusion
//    5. the kept letters keep their natural, staggered Fusion layout
//    6. the letter block is scaled to its zone and placed on the fixed plank
//  Output is FINAL size 200 x 50 x 40 mm. Do NOT scale in Bambu Studio.
//  Each order: type name1 / name2, tick add_charm if ordered, F6, then export 3MF (Colors).
// =====================================================================
// ---------------------------------------------------------------------------
// INLINED from dnp_font_metrics.scad.
// The renderer writes this template to a temp directory of its own, so an
// include <> beside it does not resolve and every glyph metric came back
// undefined - which rendered an EMPTY plank rather than failing.
// ---------------------------------------------------------------------------
// Glyph boxes at text size 10, measured in OpenSCAD 2026.09.12 on the shop PC
METRICS_LETTER_FONT = "Segoe UI Black";
METRICS_HEART_FONT = "Segoe UI Symbol";
GLYPH_METRICS = [ // [char, xmin, xmax, ymin, ymax]
  ["A", 0.0949, 10.2329, 0.0000, 9.7243],
  ["B", 0.9562, 9.2632, 0.0000, 9.7243],
  ["C", 0.4137, 8.1918, -0.1695, 9.8871],
  ["D", 0.9562, 10.1240, 0.0000, 9.7243],
  ["E", 0.9560, 7.2360, 0.0000, 9.7243],
  ["F", 0.9560, 7.0530, 0.0000, 9.7243],
  ["G", 0.4140, 9.4730, -0.1695, 9.8871],
  ["H", 0.9560, 10.1790, 0.0000, 9.7243],
  ["I", 0.9560, 3.8860, 0.0000, 9.7243],
  ["J", 0.2780, 6.2520, -0.1695, 9.7243],
  ["K", 0.9560, 9.8870, 0.0000, 9.7243],
  ["L", 0.9560, 7.2360, 0.0000, 9.7243],
  ["M", 0.9560, 12.7490, 0.0000, 9.7243],
  ["N", 0.9560, 10.3210, 0.0000, 9.7243],
  ["O", 0.4140, 10.2060, -0.1695, 9.8871],
  ["P", 0.9560, 8.9040, 0.0000, 9.7243],
  ["Q", 0.4140, 11.3180, -1.3088, 9.8871],
  ["R", 0.9560, 9.3040, 0.0000, 9.7243],
  ["S", 0.5560, 7.7100, -0.1695, 9.8871],
  ["T", 0.2100, 8.6260, 0.0000, 9.7243],
  ["U", 0.8480, 9.4330, -0.1695, 9.7243],
  ["V", 0.0680, 9.6770, 0.0000, 9.7243],
  ["W", 0.1360, 14.4850, 0.0000, 9.7243],
  ["X", 0.0000, 9.7510, 0.0000, 9.7243],
  ["Y", 0.0000, 9.0260, 0.0000, 9.7243],
  ["Z", 0.4140, 8.5720, 0.0000, 9.7243],
  ["♥", 0.6370, 8.8700, 0.8138, 8.9173]
];

// Advance width of each letter at size 10 (pen movement to the next letter).
// Measured on the shop PC with measure_pairs.scad: OpenSCAD applies NO kerning to
// Segoe UI Black, and the 'spacing' factor multiplies these advances exactly.
GLYPH_ADV = [
  ["A", 10.4170],
  ["B", 9.5960],
  ["C", 8.7490],
  ["D", 10.5390],
  ["E", 7.6630],
  ["F", 7.5140],
  ["G", 10.0980],
  ["H", 11.1420],
  ["I", 4.8420],
  ["J", 7.1010],
  ["K", 9.5830],
  ["L", 7.5140],
  ["M", 13.7060],
  ["N", 11.2780],
  ["O", 10.6270],
  ["P", 9.1080],
  ["Q", 10.6270],
  ["R", 9.4740],
  ["S", 7.9420],
  ["T", 8.8370],
  ["U", 10.2810],
  ["V", 9.7520],
  ["W", 14.6280],
  ["X", 9.7520],
  ["Y", 9.0270],
  ["Z", 8.9050]
];
// ---- end of dnp_font_metrics.scad ----

// ---------------------------------------------------------------------------
// INLINED from leaf_joint.scad.
// The renderer writes this template to a temp directory of its own, so an
// include <> beside it does not resolve and every glyph metric came back
// undefined - which rendered an EMPTY plank rather than failing.
// ---------------------------------------------------------------------------
// =====================================================================
//  LEAF JOINT - shared shape for the DNP heart snap-fit
//  Used by "heart joinery test.scad" (peg on the plank) and
//  "heart with leaf hole.scad" (hole in the heart). Both files call the
//  same modules, so a peg and a hole made with the same numbers always match.
// =====================================================================
// Leaf = a rectangle L x W with two opposite corners rounded.
// round_pct:   0 = sharp rectangle,  ~52 = the tested shape,  100 = fully round ends
function leaf_r(round_pct) = 0.7072 - max(0, min(100, round_pct)) / 100 * 0.2072;

module leaf2d(L, W, round_pct) {
    r = leaf_r(round_pct);
    scale([L, W]) intersection() {
        square(1, center = true);
        translate([-(r - 0.5), -(r - 0.5)]) circle(r = r, $fn = 120);
        translate([ (r - 0.5),  (r - 0.5)]) circle(r = r, $fn = 120);
    }
}
// positive: the peg (stands on z = 0, overlaps 0.01 below)
// base_chamfer = 45 deg fillet where the peg meets the plank (makes the peg root
// stronger - the peg no longer snaps off at the plank). The hole needs a lead-in
// chamfer at least 0.1 mm bigger than this, so the part still sits flat.
module leaf_peg(L, W, round_pct, h, chamfer, angle = 0, base_chamfer = 0) {
    rotate([0, 0, angle]) translate([0, 0, -0.01]) union() {
        hull() {
            linear_extrude(h - chamfer + 0.01) leaf2d(L, W, round_pct);
            translate([0, 0, h]) linear_extrude(0.01)
                offset(delta = -chamfer) leaf2d(L, W, round_pct);
        }
        if (base_chamfer > 0) hull() {
            linear_extrude(0.01) offset(delta = base_chamfer) leaf2d(L, W, round_pct);
            translate([0, 0, base_chamfer]) linear_extrude(0.01) leaf2d(L, W, round_pct);
        }
    }
}
// negative: the hole (mouth at z = 0, goes up to depth), gap = clearance per side
module leaf_hole(L, W, round_pct, depth, clearance, chamfer, angle = 0) {
    rotate([0, 0, angle]) translate([0, 0, -0.01]) union() {
        linear_extrude(depth + 0.01) offset(delta = clearance) leaf2d(L, W, round_pct);
        hull() {                                   // lead-in at the mouth
            linear_extrude(0.01) offset(delta = clearance + chamfer) leaf2d(L, W, round_pct);
            translate([0, 0, chamfer]) linear_extrude(0.01)
                offset(delta = clearance) leaf2d(L, W, round_pct);
        }
    }
}
// 2D dimension chart (top view): gold = peg, red line = hole outline, numbers in mm
module leaf_chart(L, W, round_pct, clearance, ph, hd, pc, hc, font = "Liberation Sans") {
    s = 10;                                        // drawn 10x so the text is readable
    scale(s) {
        color("Gold") leaf2d(L, W, round_pct);
        color("Red") difference() {
            offset(delta = clearance + 0.03) leaf2d(L, W, round_pct);
            offset(delta = clearance) leaf2d(L, W, round_pct);
        }
        color("Black") {
            translate([-L / 2, -W / 2 - 0.6]) square([L, 0.05]);           // length line
            translate([L / 2 + 0.6, -W / 2]) square([0.05, W]);            // width line
        }
    }
    color("Black") {
        translate([0, (-W / 2 - 1.3) * s]) text(str("LENGTH ", L, " mm"), size = 2.4, font = font, halign = "center");
        translate([(L / 2 + 1.0) * s, 0]) rotate(90) text(str("WIDTH ", W, " mm"), size = 2.4, font = font, halign = "center");
        translate([-L / 2 * s, (W / 2 + 1.0) * s]) for (i = [0 : 4])
            translate([0, i * 4]) text([
                str("corner round ", round_pct, " %"),
                str("peg height ", ph, " mm   peg chamfer ", pc, " mm"),
                str("hole depth ", hd, " mm   hole chamfer ", hc, " mm"),
                str("gap per side ", clearance, " mm  (red line = hole)"),
                "LEAF JOINT - top view, drawn 10x"][i], size = 2.4, font = font);
    }
}
// ---- end of leaf_joint.scad ----


/* [Order] */
name1  = "ABHAY";   // max 10 letters, A-Z
name2  = "NISHA";     // max 10 letters, A-Z
// Tick if the customer ordered a charm (it always goes on the RIGHT)
add_charm  = true;

/* [Colours (same as the Bambu Studio painting step)] */
// Colour of the letters - pick the colour in the order
text_color  = "Gold";    // [Gold, Silver, White, Black, Red, Pink, HotPink, Purple, Blue, SkyBlue, Navy, Green, Lime, Yellow, Orange, Brown, Grey]
// Colour of the plank (bottom band)
plank_color = "White";   // [Gold, Silver, White, Black, Red, Pink, HotPink, Purple, Blue, SkyBlue, Navy, Green, Lime, Yellow, Orange, Brown, Grey]
// Height painted in the plank colour, measured from the bottom. 2.4 = plank 2.3 + 0.1,
// so the printer changes colour only ONCE (less purge / poop waste)
color_split = 2.4;
// What to export.
// MAIN WAY (one file, already coloured):  leave on "all", press F6, then
//   File > Export > Export as 3MF...  and in the dialog set
//   Format > "Export colors as" = Colors  (NOT "Base Material"), then OK.
//   Bambu Studio opens it as ONE object: plank colour 0 - 2.4 mm, text colour above.
// Backup way: export part 1 and part 2 as two STLs and load them together in Bambu.
export_part = "all";     // [all:Both colours - export as 3MF (Colors), plank_part:1 - Plank colour part (STL), text_part:2 - Text colour part (STL), test_plank:CHARM FIT TEST - plank strip with 6 peg pairs (3MF Colors), test_blocks:CHARM FIT TEST - 6 charm blocks with holes (STL), joint_piece:Joint piece - small cut of the plank with the pegs (3MF Colors), leaf_chart:Leaf joint chart - top view drawing (F5 only)]

/* [Charm joint - placement] */
// Number of pegs in the charm space (2 = the charm cannot turn)
charm_pegs      = 2;      // [1:1:3]
// UNIVERSAL CHARM SPACING = 11 mm (centre to centre). All 5 charm files (C1-C5 leaf holes)
// are made for 11 mm - do NOT change it, or the charms will not fit.
// (0 = old rule: 40/3 = 13.33 mm, too wide for C4 / C5)
peg_spacing     = 11;     // [0:0.5:30]
// Pegs run along the plank (0) or across it (90)
peg_row_angle   = 0;      // [0:90:90]
peg_offset_x    = 0;      // move the peg group along the plank, mm
peg_offset_y    = 0;      // move the peg group across the plank, mm

/* [Leaf joint chart - SHAPE (same numbers in the charm files!)] */
// Leaf length (along its long side), mm. Heart test: 4.0
leaf_length    = 4.0;    // [2:0.1:6]
// Leaf width, mm. Heart test: 4.0
leaf_width     = 4.0;    // [2:0.1:6]
// Corner rounding: 0 = sharp rectangle, 52 = tested leaf, 100 = round ends
leaf_round     = 51.7;   // [0:1:100]
// Turn the leaf on its own centre, deg (0 = as tested)
leaf_angle     = 0;      // [0:5:180]

/* [Leaf joint chart - PEG on the plank] */
// Peg height above the plank, mm. Tested: 3.8 (keep 0.2 below hole depth)
peg_height     = 3.8;    // [1:0.1:8]
// Lead-in chamfer on the peg top, mm. Tested: 0.4
peg_chamfer    = 0.4;    // [0:0.05:1]
// Strengthening chamfer at the peg ROOT (where it joins the plank), mm, 45 deg.
// 0 = old sharp corner (pegs snapped off). Keep hole_chamfer = this + 0.1
peg_base_chamfer = 0.5;  // [0:0.05:1]

/* [Leaf joint chart - HOLE in the charm] */
// Hole depth, mm. Tested: 4.0
hole_depth     = 4.0;    // [1:0.1:9]
// Gap per side between peg and hole, mm. Charms print with "Ghost Charm Settings"
// (0.12 mm layers), so this is NOT the heart value. Set it from the charm fit test.
// Heart at 0.28 OPT DNP was 0.08. Charm test gaps: -0.03 -0.02 -0.01 0 0.02 0.04
fit_clearance  = 0.04;   // [-0.1:0.01:0.3]  CHARM GAP = 0.04 mm per side (all 5 charm files use 0.04)
// Chamfer at the hole mouth, mm. Must be peg_base_chamfer + 0.1 so the part sits flat
// over the peg root chamfer (was 0.3 before the root chamfer was added)
hole_chamfer   = 0.6;    // [0:0.05:1]

/* [Charm fit test] */
// Gaps to test, mm per side (engraved as hundredths: -3 = -0.03)
test_clearances = [-0.03, -0.02, -0.01, 0, 0.02, 0.04];
// Size of each test block (stands in for the bottom of a charm), mm
test_block_w = 22;
test_block_d = 12;
test_block_h = 7;

/* [Lettering - same as the Fusion text box] */
font_height  = 25;    // mm, letter (cap) height before fitting
char_spacing = 7;     // Fusion "character spacing", % (OpenSCAD spacing = 1.07)
letter_font  = "Segoe UI Black";

/* [Product dimensions (mm)] */
plank_length    = 200;
plank_width     = 50;
plank_thickness = 2.3;
total_height    = 40;    // plank bottom to letter top
letter_sink     = 1.0;   // how deep letters go into the plank
zone_left       = 8;     // empty space on the left (and right when there is no charm)
zone_charm      = 40;    // space on the right for the charm
letter_width    = 40;    // width of the letter block (centred on the plank)
fit_to_plank    = true;  // false = export the natural 25 mm model, unscaled

/* [Filler hearts (from the reference file)] */
heart_drop  = 3;      // mm the filler heart is grown + pushed down (stronger base)
desc_keep   = 0.02;   // part of a glyph kept below the baseline (x cap height)

/* [Hidden] */
$fn = 64;
MAX_LETTERS = 10;
HEART = "*";                        // internal marker for a filler heart
U    = 10;                          // glyphs are built at text size 10 ...
K    = font_height / 9.7243;        // ... then scaled so a cap is font_height mm
CAP  = 9.7243;                      // cap height at size 10 (measured "H")
SP   = 1 + char_spacing / 100;      // OpenSCAD text spacing factor
LSB_H = 0.956;  RSB_H = 11.142 - 10.179;   // side bearings of "H" (size 10)

HEART_POLY = [
    [0.4243,0.4953],[0.4332,0.5110],[0.4433,0.5274],[0.4529,0.5446],[0.4628,0.5627],
    [0.4706,0.5813],[0.4782,0.6006],[0.4842,0.6204],[0.4898,0.6408],[0.4949,0.6620],
    [0.5000,0.6839],[0.5046,0.7067],[0.5073,0.7296],[0.5078,0.7522],[0.5054,0.7741],
    [0.5015,0.7958],[0.4965,0.8173],[0.4905,0.8388],[0.4828,0.8596],[0.4731,0.8794],
    [0.4616,0.8979],[0.4477,0.9145],[0.4325,0.9299],[0.4159,0.9437],[0.3996,0.9577],
    [0.3822,0.9703],[0.3638,0.9813],[0.3434,0.9891],[0.3221,0.9949],[0.3000,0.9982],
    [0.2776,0.9999],[0.2552,1.0000],[0.2332,0.9992],[0.2114,0.9972],[0.1899,0.9938],
    [0.1685,0.9884],[0.1472,0.9805],[0.1260,0.9690],[0.1054,0.9553],[0.0854,0.9379],
    [0.0667,0.9194],[0.0489,0.8964],[0.0323,0.8676],[0.0177,0.8376],[0.0054,0.8163],
    [-0.0059,0.8187],[-0.0182,0.8386],[-0.0329,0.8692],[-0.0490,0.8940],[-0.0667,0.9163],
    [-0.0849,0.9330],[-0.1052,0.9522],[-0.1259,0.9672],[-0.1476,0.9801],[-0.1681,0.9858],
    [-0.1884,0.9887],[-0.2084,0.9888],[-0.2295,0.9901],[-0.2517,0.9920],[-0.2753,0.9947],
    [-0.2989,0.9956],[-0.3220,0.9940],[-0.3438,0.9892],[-0.3644,0.9815],[-0.3835,0.9713],
    [-0.4016,0.9595],[-0.4184,0.9460],[-0.4342,0.9311],[-0.4480,0.9144],[-0.4607,0.8968],
    [-0.4723,0.8783],[-0.4836,0.8599],[-0.4928,0.8401],[-0.4994,0.8190],[-0.5032,0.7966],
    [-0.5057,0.7740],[-0.5072,0.7517],[-0.5078,0.7296],[-0.5061,0.7071],[-0.5021,0.6846],
    [-0.4967,0.6624],[-0.4905,0.6409],[-0.4843,0.6203],[-0.4761,0.6000],[-0.4664,0.5803],
    [-0.4549,0.5613],[-0.4418,0.5432],[-0.4290,0.5261],[-0.4160,0.5100],[-0.4067,0.4950],
    [-0.3985,0.4807],[-0.3919,0.4668],[-0.3846,0.4534],[-0.3771,0.4405],[-0.3705,0.4278],
    [-0.3629,0.4157],[-0.3563,0.4038],[-0.3478,0.3926],[-0.3407,0.3814],[-0.3320,0.3710],
    [-0.3251,0.3603],[-0.3175,0.3501],[-0.3108,0.3398],[-0.3035,0.3299],[-0.2956,0.3205],
    [-0.2885,0.3108],[-0.2807,0.3017],[-0.2735,0.2922],[-0.2652,0.2837],[-0.2582,0.2743],
    [-0.2512,0.2649],[-0.2447,0.2548],[-0.2371,0.2457],[-0.2298,0.2363],[-0.2223,0.2267],
    [-0.2153,0.2164],[-0.2079,0.2061],[-0.2004,0.1957],[-0.1929,0.1847],[-0.1854,0.1729],
    [-0.1777,0.1607],[-0.1696,0.1482],[-0.1611,0.1352],[-0.1526,0.1209],[-0.1436,0.1060],
    [-0.1342,0.0901],[-0.1241,0.0736],[-0.1131,0.0575],[0.0000,0.0000]
];


// ---- names -------------------------------------------------------------
function upper_c(c) = (ord(c) >= 97 && ord(c) <= 122) ? chr(ord(c) - 32) : c;
function clean(s) = [for (c = s) if (upper_c(c) != " ") upper_c(c)];
function hearts(n) = [for (i = [0:1:n-1]) if (n > 0) HEART];
// shorter name: floor(d/2) hearts in front, the rest at the end
function fill(l, d) = concat(hearts(floor(d/2)), l, hearts(d - floor(d/2)));
function join(l, i = 0) = i >= len(l) ? "" : str(l[i], join(l, i + 1));

A0 = clean(name1);  B0 = clean(name2);
N  = max(len(A0), len(B0));
A  = fill(A0, N - len(A0));
B  = fill(B0, N - len(B0));

// ---- glyph sizes (size-10 units) ----------------------------------------
function m(c)   = [for (g = GLYPH_METRICS) if (g[0] == c) g][0];
function adv(c) = [for (g = GLYPH_ADV) if (g[0] == c) g[1]][0];
HDROP = heart_drop / K;                     // drop in size-10 units
HK    = (CAP + HDROP) / CAP;                // heart grows so its top stays at CAP
HW    = 1.0156 * CAP * HK;                  // heart width
function g_lsb(c) = c == HEART ? LSB_H : m(c)[1];
function g_w(c)   = c == HEART ? HW    : m(c)[2] - m(c)[1];
function g_adv(c) = c == HEART ? LSB_H + HW + RSB_H : adv(c);

// ---- whole-line layout (exactly how text() places letters) --------------
// pen(i) = SP * sum of advances before i  (verified on the PC: no kerning)
function pen(l, i) = i <= 0 ? 0 : pen(l, i - 1) + SP * g_adv(l[i - 1]);
function ink0(l, i) = pen(l, i) + g_lsb(l[i]);
function ink1(l, i) = ink0(l, i) + g_w(l[i]);
// cut lines halfway through each gap - used to pick the right blocks
function cut(l, i) = i <= 0 ? -1e4 : i >= len(l) ? 1e4
                   : (ink1(l, i - 1) + ink0(l, i)) / 2;

// first / last real letter of a padded name
function first_letter(l, i = 0) = l[i] != HEART ? i : first_letter(l, i + 1);

// ---- 2D whole-name line --------------------------------------------------
module heart2d() {
    translate([HW / 2, -HDROP]) scale(CAP * HK) polygon(HEART_POLY);
}
module name_line(l) {
    f = first_letter(l);
    letters = join([for (c = l) if (c != HEART) c]);
    // the letters: ONE text() call for the whole name
    translate([pen(l, f), 0])
        text(letters, size = U, font = letter_font, spacing = SP,
             halign = "left", valign = "baseline");
    // filler hearts in front of / behind the letters
    for (i = [0:len(l) - 1]) if (l[i] == HEART)
        translate([ink0(l, i), 0]) heart2d();
}

// ---- 3D: whole names extruded through each other ------------------------
EXT = 1000;
module sweep_name1() {           // name 1 faces -Y, reads along +X
    rotate([90, 0, 0]) linear_extrude(EXT, center = true) name_line(A);
}
module sweep_name2() {           // name 2 faces +X, reads along +Y
    rotate([90, 0, 90]) linear_extrude(EXT, center = true) name_line(B);
}
// the blocks to keep: letter i of name 1 crossed with letter i of name 2
module keep_boxes() {
    for (i = [0:N - 1])
        translate([cut(A, i), cut(B, i), -desc_keep * CAP])
            cube([cut(A, i + 1) - cut(A, i), cut(B, i + 1) - cut(B, i), CAP * 2]);
}
module raw_block() {
    intersection() {
        sweep_name1();
        sweep_name2();
        keep_boxes();      // = picking the correct blocks in Fusion
    }
}

// ---- size of the kept block after turning it along the plank -------------
R2 = sqrt(2);
U_MIN = min([for (i = [0:N-1]) (ink0(A, i) + ink0(B, i)) / R2]);
U_MAX = max([for (i = [0:N-1]) (ink1(A, i) + ink1(B, i)) / R2]);
V_MIN = min([for (i = [0:N-1]) (ink0(B, i) - ink1(A, i)) / R2]);
V_MAX = max([for (i = [0:N-1]) (ink1(B, i) - ink0(A, i)) / R2]);
Z_MIN = -desc_keep * CAP;

// ---- target size from the heart choice ----------------------------------
Z_LEFT  = zone_left;
Z_RIGHT = add_charm ? zone_charm : zone_left;
BLOCK_L = plank_length - Z_LEFT - Z_RIGHT;       // 152 with charm, 184 without
BLOCK_W = letter_width;                          // 40
BLOCK_Z0 = plank_thickness - letter_sink;        // baseline height, 1.3
BLOCK_H = total_height - BLOCK_Z0;               // baseline -> top, 38.7

SX = fit_to_plank ? BLOCK_L / (U_MAX - U_MIN) : K;
SY = fit_to_plank ? BLOCK_W / (V_MAX - V_MIN) : K;
SZ = fit_to_plank ? BLOCK_H / CAP             : K;

NAT_L = (U_MAX - U_MIN) * K;                     // natural (unfitted) sizes, mm
NAT_W = (V_MAX - V_MIN) * K;

// render() turns the letters into a finished mesh before they are shown.
// Without it the F5 preview needs OpenCSG shader files that the standalone
// openscad.exe cannot find ("Cannot open shader source file 'OpenCSG.vert'"),
// so the preview showed only the plank. F6/STL export were never affected.
module letters() { render() letters_raw(); }
module letters_raw() {
    translate([Z_LEFT, fit_to_plank ? (plank_width - BLOCK_W) / 2 : 5, BLOCK_Z0])
    scale([SX, SY, SZ])
    translate([-U_MIN, -V_MIN, 0])
    rotate([0, 0, -45])
        raw_block();
}
module plank_slab() {
    if (fit_to_plank) cube([plank_length, plank_width, plank_thickness]);
    else cube([Z_LEFT + NAT_L + Z_RIGHT, NAT_W + 10, plank_thickness]);
}
module plank() {
    plank_slab();
    if (add_charm) for (p = charm_pegs_xy()) translate([p[0], p[1], plank_thickness]) peg();
}

// ---- charm joint -------------------------------------------------------------
CHARM_C  = [plank_length - Z_RIGHT / 2 + peg_offset_x, plank_width / 2 + peg_offset_y];
PEG_STEP = peg_spacing > 0 ? peg_spacing : zone_charm / (charm_pegs + 1);
// peg positions relative to the charm centre (the SAME list makes the charm holes)
function peg_pattern() = [for (k = [0 : charm_pegs - 1])
    let(t = (k - (charm_pegs - 1) / 2) * PEG_STEP)
    [t * cos(peg_row_angle), t * sin(peg_row_angle)]];
function charm_pegs_xy() = [for (q = peg_pattern()) [CHARM_C[0] + q[0], CHARM_C[1] + q[1]]];

module peg()  leaf_peg(leaf_length, leaf_width, leaf_round, peg_height, peg_chamfer, leaf_angle, peg_base_chamfer);
module hole(c = fit_clearance)
    leaf_hole(leaf_length, leaf_width, leaf_round, hole_depth, c, hole_chamfer, leaf_angle);
// ---- CHARM FIT TEST ----------------------------------------------------------------
//  test_plank : a strip of real plank (2.3 mm, colour change at 2.4) with 6 PAIRS of pegs,
//               spaced exactly like on the DNP. Print with the DNP preset (0.28 OPT DNP).
//  test_blocks: 6 blocks with 2 holes each = the bottom of a charm.
//               Print with "Ghost Charm Settings" (0.12 mm), like the real charms.
//  The gap is engraved: -3 = -0.03 mm, 0 = 0.00, 2 = 0.02, 4 = 0.04 (per side)
TP = test_block_w + 4;            // spacing between test positions, mm
module test_label(i, sz) text(str(round(test_clearances[i] * 100)), size = sz,
        font = letter_font, halign = "center", valign = "center");
module test_strip_whole() {
    n = len(test_clearances);
    difference() {
        cube([n * TP + 4, 22, plank_thickness]);
        for (i = [0 : n - 1]) translate([2 + TP / 2 + i * TP, 3.5, plank_thickness - 0.4])
            linear_extrude(1) test_label(i, 3.5);
    }
    for (i = [0 : n - 1], q = peg_pattern())
        translate([2 + TP / 2 + i * TP + q[0], 13 + q[1], plank_thickness]) peg();
}
module test_strip() {
    color(plank_color) render() intersection() { test_strip_whole(); band(-1, color_split); }
    color(text_color)  render() intersection() { test_strip_whole(); band(color_split, 50); }
}
module test_blocks() {
    n = len(test_clearances);
    for (i = [0 : n - 1]) translate([2 + TP / 2 + i * TP, test_block_d / 2 + 2, 0]) difference() {
        translate([-test_block_w / 2, -test_block_d / 2, 0]) cube([test_block_w, test_block_d, test_block_h]);
        for (q = peg_pattern()) translate([q[0], q[1], 0]) hole(test_clearances[i]);
        translate([0, 0, test_block_h - 0.5]) linear_extrude(1) test_label(i, 4.5);
    }
}

// ---- joint piece: a small cut of the real plank around the right-hand heart ------
joint_piece_size = 38;            // mm, square piece centred on the pegs (clear of the letters)
module joint_piece_whole() {
    p = CHARM_C;
    intersection() {
        plank();                  // plank + peg only, no letters
        translate([p[0] - joint_piece_size / 2, p[1] - joint_piece_size / 2, -1])
            cube([joint_piece_size, joint_piece_size, 60]);
    }
}
module joint_piece() {
    color(plank_color) render() intersection() { joint_piece_whole(); band(-1, color_split); }
    color(text_color)  render() intersection() { joint_piece_whole(); band(color_split, 60); }
}

// ---- checks + report -----------------------------------------------------
assert(len(A0) > 0 && len(B0) > 0, "Both names are required");
assert(len(A0) <= MAX_LETTERS && len(B0) <= MAX_LETTERS, "Max 10 letters per name");
echo(str("DNP  Name1 = ", join(A), "   Name2 = ", join(B), "   (* = heart)   charm: ", add_charm));
echo(str("DNP  natural letter block at ", font_height, " mm: ", NAT_L, " x ", NAT_W, " mm"));
echo(str("DNP  zones L/R = ", Z_LEFT, "/", Z_RIGHT, " mm, letter block = ",
         fit_to_plank ? BLOCK_L : NAT_L, " x ", fit_to_plank ? BLOCK_W : NAT_W,
         " x ", total_height, " mm"));

// ---- output: split in two colours at color_split ---------------------------
// Same as Bambu: paint tool -> Height range -> from the bottom, 0 to 2.4 mm = plank
// colour; everything above = letter colour. Done here, so it arrives coloured.
module whole() { union() { plank(); letters(); } }
module band(z0, z1) {               // a slab covering the whole model between z0 and z1
    translate([-50, -50, z0]) cube([plank_length + 1000, plank_width + 1000, z1 - z0]);
}
module plank_part() { render() intersection() { whole(); band(-1, color_split); } }
module text_part()  { render() intersection() { whole(); band(color_split, total_height + 10); } }

echo(str("DNP  colours: ", plank_color, " up to ", color_split, " mm, ", text_color, " above"));

echo(str("DNP  charm joint: ", add_charm ? charm_pegs : 0, " pegs, ", PEG_STEP, " mm apart, leaf ", leaf_length, " x ", leaf_width,
         " mm, peg ", peg_height, " mm, hole ", hole_depth, " mm, gap ", fit_clearance, " mm/side, pegs at ", charm_pegs_xy()));

if (export_part == "test_plank")       test_strip();
else if (export_part == "test_blocks") color("White") test_blocks();
else if (export_part == "joint_piece") joint_piece();
else if (export_part == "leaf_chart")  leaf_chart(leaf_length, leaf_width, leaf_round, fit_clearance,
                                                  peg_height, hole_depth, peg_chamfer, hole_chamfer, letter_font);
else if (export_part == "joint_piece_1") intersection() { joint_piece_whole(); band(-1, color_split); }
else if (export_part == "joint_piece_2") intersection() { joint_piece_whole(); band(color_split, 60); }
else if (export_part == "plank_part") color(plank_color) plank_part();
else if (export_part == "text_part")  color(text_color)  text_part();
else {
    color(plank_color) plank_part();
    color(text_color)  text_part();
}
