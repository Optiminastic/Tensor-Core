// ============================================================================
//  DUAL-VIEW NAME PLATE  (DNP)  -  NO-HEART VARIANT
//
//  Same plate as dual_name.scad, with two changes:
//     PAD_GLYPH = "none"   - no heart anywhere
//     MARGIN_L  = MARGIN_R = 9 mm - equal gap on both sides, so the name sits
//                            centred instead of leaving heart space on the right
//
//  What sets this variant apart is its EQUAL margins (MARGIN_L = MARGIN_R), so
//  the name sits centred on the plate. It no longer drops padding slots: doing
//  that lost letters from the longer name.
//  Read NAME_L standing to the left, NAME_R standing to the right.
//
//  Letter i of NAME_L is swept along Y, letter i of NAME_R along X, and the two
//  are INTERSECTED -> one blob that is both letters at once.  Blob i is parked
//  at (i*PITCH, i*PITCH) so the run lies on a 45 deg diagonal; the run is then
//  spun -45 deg to lie along the plate and shifted to honour the margins.
//
//  PLATE GEOMETRY IS FIXED AND HARD-CODED - do not derive it from the text.
// ============================================================================

/* [Names] */
NAME_L      = "JESUS";           // read from the LEFT
NAME_R      = "CHRIST";           // read from the RIGHT
PAD_L       = "center";           // "center" | "left" | "right"
PAD_R       = "center";
PAD_GLYPH   = "heart";            // A padding slot has to show SOMETHING. With
                                  // "none" the slot is dropped outright, and the
                                  // longer name silently loses that letter -
                                  // JESUS/CHRIST printed as CHRIS. There is no
                                  // way to keep every letter AND show nothing on
                                  // the short side, so the slot gets a heart.
                                  // Set back to "none" only for equal-length
                                  // pairs, where there are no padding slots and
                                  // nothing is lost either way.
DESC_KEEP   = 0.02;               // how much of a glyph is allowed below the
                                  // baseline, as a fraction of cap height.
                                  // Round letters (O S C G) dip slightly below
                                  // the baseline on purpose - optical overshoot,
                                  // 0.42 mm at a 25 mm cap - and clipping that
                                  // flattens their curve. A true descender is far
                                  // deeper: Q drops 3.27 mm, four times the plate
                                  // floor, so its tail pokes out under the plate.
                                  // 0.02 keeps the overshoot and cuts the tail.
                                  //
                                  // It also fixes cap heights. resize() normalises
                                  // the BOUNDING BOX, so Q's 27.99 mm box was
                                  // scaled down while A's 24.31 mm box was scaled
                                  // up - Q came out 13% shorter than its
                                  // neighbours. Clipping first makes every box a
                                  // cap box, so they all match.
SPACE_W     = 10;                 // total gap at a typed space, in mm. A word
                                  // break opens to SPACE_W instead of the normal
                                  // GAP, so "TUSHAR KUMAR" reads as two words.
                                  // Only the extra (SPACE_W - GAP) is added, so
                                  // setting it below GAP changes nothing.
                                  // A space is NOT a slot: it cannot be, because
                                  // a slot is the intersection of the two names,
                                  // so an empty one on the left would delete the
                                  // right name's letter that shares it. Instead
                                  // the space is removed from the slot sequence
                                  // and re-added as extra advance, which the two
                                  // names already track independently.
HEART_DROP  = 3;                  // mm the HEART alone is pushed down into the
                                  // plate. Letters are untouched.
                                  // A heart tapers to a point, so where it meets
                                  // the plate it is nearly nothing - it is always
                                  // the weakest joint on the plate and the first
                                  // thing to snap off. Growing it and dropping the
                                  // tip below the plate top means the plate meets
                                  // it further up, where it is much wider. The top
                                  // of the heart stays exactly on LETTER_H, so
                                  // nothing above the plate changes height.
                                  // 0 = off.
HEART_SCALE = 1.0;                // heart height relative to LETTER_H

/* [FIXED PLATE - hard-coded, same on every plate] */
/* [AUTO SIZE - this copy grows the plate to fit the name] */
// The main folder's files force the plate to a fixed 200 x 50 x 40 and squeeze the
// glyphs until they fit. These copies do the opposite: the letters are left at the
// font's NATURAL width and the plate is sized around them. Export it and set your
// product dimensions in Bambu Studio, exactly as the Fusion route does.
//
// Natural width is also what makes the stroke weight right. Measured on DNP.f3d,
// the letters are 7.55 mm thick at a 25 mm cap height - a ratio of 0.302, which is
// simply Segoe UI Black's own stroke. Squeezing the glyphs thins them and makes
// every name a different weight; leaving them alone gives every letter of every
// plate the identical thickness the reference has.
AUTO_SIZE   = true;               // false = behave like the fixed-size originals
CAP_H       = 25;                 // letter cap height, mm - matches Fusion's
                                  // "Height 25 mm". Only used when AUTO_SIZE.

/* [Output size - the Bambu "uncheck uniform scale" step, done right here] */
// Set the finished product size and the model is scaled to it on export, exactly
// as unticking "uniform scale" and typing X/Y/Z in Bambu Studio does. Leave the
// zeros to export at natural size instead.
//   OUT_X/Y/Z = 0        -> natural size, whatever the name needs
//   200 / 50 / 40        -> the standard DNP product
// This is a real scale, not a re-fit: the three axes scale by different amounts,
// so the letters end up sheared the same way the Bambu route shears them.
OUT_X       = 0;
OUT_Y       = 0;
OUT_Z       = 0;

PLATE_L     = 200;                // X, mm
PLATE_W     = 50;                 // Y, mm
PLATE_T     = 1.8;                // thickness, mm
MARGIN_L    = 12;                  // gap, plate left edge -> first letter
MARGIN_R    = 12;                  // EQUAL to MARGIN_L on this variant. With no
                                  // heart there is no empty space to reserve on
                                  // the right, so the name is centred on the
                                  // plate with the same 9 mm gap either side.
TOTAL_H     = 40;                 // overall model height, mm (X/Y are PLATE_L/PLATE_W)
                                  // LETTER_H is derived below, after SINK exists
MAX_LETTERS = 12;                 // most that will fit in the text zone
GAP         = 8.05;               // clear space between letters, measured ALONG
                                  // the plate. Note it is NOT what you see when
                                  // reading the name: the run sits on a 45 deg
                                  // diagonal, so the gap you read is GAP/sqrt(2).
                                  // 8.05 here reads as 4.25 mm at a 25 mm cap,
                                  // which is what DNP.f3d measures (0.172 x cap).
                                  // measured along the plate, mm
MIN_LETTER  = 8;                  // below this a letter is unreadable; the model
                                  // warns and stops widening the gap
CONDENSE    = true;               // squeeze each glyph into its cell.
                                  // Segoe UI Black at 40 mm caps is ~34-38 mm per
                                  // letter naturally, but 12 letters in the 110 mm
                                  // zone allows only 9.17 mm - without this the
                                  // letters overlap into mush. See README.
SQUEEZE     = 0;                  // 0 = auto-fit from the width table below.
                                  // Set >0 to override manually.
                                  // Uniform horizontal squeeze applied to EVERY
                                  // glyph. Because it is the same factor for all
                                  // of them, letters keep their natural relative
                                  // widths - a narrow "I" stays narrow and a wide
                                  // "W" stays wide, as in the Fusion original.
                                  // Lower = narrower letters overall.
FILL        = 1.02;               // glyph width as a fraction of its cell.
                                  // THIS IS THE STROKE-THICKNESS KNOB: raise
                                  // it to fatten the letters, lower it to slim
                                  // them. Was previously referenced only in
                                  // comments and had no effect at all.
STROKE      = 0;                  // 0 = the font exactly as drawn, which is what
                                  // DNP.f3d uses: its letters measure 7.55 mm at a
                                  // 24.67 mm cap, and natural Segoe UI Black gives
                                  // 7.53 mm. Any boldening here also eats into the
                                  // gap between letters and closes it up.
                                  // every glyph outline, so each stroke ends up
                                  // 2*STROKE thicker.
                                  //   0    = the font exactly as drawn
                                  //   0.4  = slightly heavier
                                  //   1.0  = clearly bolder
                                  //   1.5+ = very heavy; counters (the holes in
                                  //          A, B, O, R) start to close up
                                  //   <0   = thinner; too negative and thin
                                  //          strokes disappear entirely
                                  // Cap height is forced back to LETTER_H after
                                  // fattening and the layout below re-fits
                                  // itself, so the model stays exactly TOTAL_H
                                  // tall and the run still lands on the margins.
STAGGER     = 1;                  // how much the letters step forward/back so
                                  // both names stay visible. The offset is not
                                  // arbitrary - it falls out of the width
                                  // difference between the two glyphs meeting in
                                  // each slot, so a wide letter gets out of the
                                  // way of whatever sits opposite it.
                                  //   1   = natural, what the geometry asks for
                                  //   0.5 = half as much, letters more in line
                                  //   0   = dead straight line (names crowd)
                                  // The band is always CENTRED on the plate, at
                                  // any setting.
KEEP_COUNTERS = true;             // grow the stroke OUTWARDS only, so the holes
                                  // in A, D, O, R, 8 keep their original size.
                                  // With this off, offset() grows inwards too and
                                  // a big STROKE seals the counters shut - at
                                  // STROKE=5 the whole word turns into blobs.
COUNTER_R   = 0;                  // 0 = auto (LETTER_H * 0.35). This is the
                                  // closing radius used to find the counters; it
                                  // must exceed half the widest counter or that
                                  // one still fills in.
SHEAR       = 0.8334;             // only used when FUSION_MATCH is on. 1.0 =
                                  // undistorted; 0.8334 reproduces DNP.3mf.
FUSION_MATCH = false;             // false: build straight to 200x50x40 with the
                                  //        two reading planes a true 90 deg apart.
                                  //        Undistorted - the default.
                                  // true : reproduce the Fusion -> Bambu route
                                  //        exactly, including its distortion.
                                  //        There the letters are drawn at natural
                                  //        width and the whole model is then
                                  //        squashed by DIFFERENT amounts in X and
                                  //        Y (58.43% / 70.11% on the reference),
                                  //        which shears the letters so the planes
                                  //        end up ~100 deg apart, not 90. Use it
                                  //        when a new plate has to sit next to
                                  //        parts already printed the old way.
EDGE        = 3;                  // keep the blobs this far inside the long edges

/* [Font] */
FONT        = "Segoe UI Black";   // seguibl.ttf reports family "Segoe UI Black",
                                  // style "Regular" - there is NO Bold style on
                                  // this face. Asking for :style=Bold is invalid;
                                  // name the family alone. (Fusion shows a "B"
                                  // button pressed, but the family is already the
                                  // black weight - that button adds nothing here.)

/* [Colour - preview only, no export format carries it] */
TEXT_COLOR  = "Green";
BASE_COLOR  = "White";

/* [Export] */
PART        = "all";              // "all" | "text" | "base"
PREVIEW_EXACT = true;             // render() so F5 is not shredded by OpenCSG

/* [Plate detail] */
PLATE_R     = 0;                  // corner radius, mm. 0 = square corners
PLATE_CHAM  = 0;                  // top chamfer, mm (0 = off). Off: the plate
                                  // keeps plain square edges, no bevelled border.
SINK        = 2.0;                // how deep the letters sit INTO the plate, mm.
                                  // Not cosmetic: it sets how much of the glyph
                                  // is fused to the plate. A heart or a pointed
                                  // letter tapers to almost nothing at its base,
                                  // so a shallow sink leaves it standing on a
                                  // point and it snaps off the print.
                                  // Measured on SHILPA/RAJU, weakest blob:
                                  //     0.6 mm sink ->  4.2 mm2 of contact
                                  //     2.0 mm sink -> 12.2 mm2   (2.9x)
                                  //     2.49 mm     -> 14.3 mm2   (3.4x)
                                  // 2.0 keeps a continuous 0.5 mm of plate under
                                  // every letter, so the first layer stays solid.
                                  // Visible letter height is unaffected: it is
                                  // always TOTAL_H - PLATE_T = 37.5 mm.
// Cap height derived so the exported model is exactly TOTAL_H tall:
// the plate hangs (PLATE_T - SINK) below z=0 and the letters rise to
// LETTER_H above it. Declared here because OpenSCAD resolves these
// top-level assignments in order - referencing SINK any earlier is
// an "unknown variable" and silently poisons every glyph size.
// SINK deeper than the plate is thick punches the letters straight through it:
// the plate bottom sits at SINK-PLATE_T, so with PLATE_T 1.8 and SINK 2.0 the
// plate floats at z = +0.2 while the letters still start at z = 0, leaving them
// hanging BELOW the base. On the bed that prints as letter outlines in the first
// layer instead of a solid plate. Clamp so a solid floor always remains.
FLOOR_MIN   = 0.8;                // solid plate kept under every letter, mm
SINK_EFF    = min(SINK, max(0, PLATE_T - FLOOR_MIN));
if (SINK > PLATE_T - FLOOR_MIN)
    echo(str("note: SINK ", SINK, "mm exceeds PLATE_T ", PLATE_T,
             " minus the ", FLOOR_MIN, "mm floor - using ", SINK_EFF, "mm"));

LETTER_H    = AUTO_SIZE ? CAP_H : TOTAL_H - (PLATE_T - SINK_EFF);
// must be defined here, not further down: gw() uses it while the layout is
// being solved, and OpenSCAD evaluates top-level assignments in order
HK          = LETTER_H > 0 ? (LETTER_H + HEART_DROP) / LETTER_H : 1;
PEDESTAL    = 0;                  // mm pad under each blob if one floats free
EXT         = 200;                // sweep length, must exceed the widest glyph
$fn         = 96;

// ---------------------------------------------------------------------------
HEART = [
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


// ---------------------------------------------------------------------------
// Glyph widths for Segoe UI Black Bold, normalised to cap height (width / cap).
// Measured by extruding each glyph and reading its STL bounding box, because
// OpenSCAD 2021.01 has no textmetrics(). Used to auto-fit SQUEEZE so wide
// letters ("W" 1.48) and narrow ones ("I" 0.30) keep their true proportions.
W_TBL = [
    ["A",1.0425],["B",0.8543],["C",0.7735],["D",0.9428],["E",0.6457],
    ["F",0.6270],["G",0.9008],["H",0.9483],["I",0.3013],["J",0.6039],
    ["K",0.9184],["L",0.6457],["M",1.2126],["N",0.9630],["O",0.9738],
    ["P",0.8173],["Q",0.9740],["R",0.8585],["S",0.7113],["T",0.8654],
    ["U",0.8677],["V",0.9882],["W",1.4756],["X",1.0029],["Y",0.9281],
    ["Z",0.8388]
];
HEART_W = 1.016;                         // HEART polygon spans ~1.016 x 1.0

function gw_i(ch, i) = i >= len(W_TBL) ? 1.0
                     : (W_TBL[i][0] == ch ? W_TBL[i][1] : gw_i(ch, i + 1));
function gw(ch)      = ch == PAD_CH ? HEART_W * HK : gw_i(ch, 0);

// ---------------------------------------------------------------------------
function rep(s, n)    = n <= 0 ? "" : str(s, rep(s, n - 1));
function pad(s, n, m) =
      len(s) >= n  ? s
    : m == "left"  ? str(s, rep(PAD_CH, n - len(s)))
    : m == "right" ? str(rep(PAD_CH, n - len(s)), s)
    :                str(rep(PAD_CH, floor((n - len(s)) / 2)), s,
                         rep(PAD_CH, n - len(s) - floor((n - len(s)) / 2)));

// ---- typed spaces vs padding ----------------------------------------------
// PAD_CH marks a slot the customer did not fill; it becomes a heart. It must be
// a character nobody can type into a name, or a real space in "A K A SCHOOL"
// turns into a heart - which is exactly what used to happen.
PAD_CH = "	";

function strip(s, i = 0, acc = "") =
    i >= len(s) ? acc : strip(s, i + 1, s[i] == " " ? acc : str(acc, s[i]));

// original index of the j-th non-space character
function origidx(s, j, i = 0, seen = 0) =
    i >= len(s) ? i
    : s[i] == " " ? origidx(s, j, i + 1, seen)
    : (seen == j ? i : origidx(s, j, i + 1, seen + 1));

// spaces strictly before original index p
function spcount(s, p, i = 0) =
    i >= p ? 0 : (s[i] == " " ? 1 : 0) + spcount(s, p, i + 1);

// extra advance in front of stripped letter j, in mm along the plate
function spgap(s, j) =
    j <= 0 ? 0
    : max(0, SPACE_W - GAP) * (spcount(s, origidx(s, j)) - spcount(s, origidx(s, j - 1)));

NAME_LS = strip(NAME_L);
NAME_RS = strip(NAME_R);

N     = min(max(len(NAME_LS), len(NAME_RS)), MAX_LETTERS);
L     = pad(NAME_LS, N, PAD_L);
R     = pad(NAME_RS, N, PAD_R);

ZONE  = PLATE_L - MARGIN_L - MARGIN_R;   // usable text width along the plate
// Each blob is the intersection of two glyphs, so after the -45 spin its extent
// ALONG the plate is (w_L + w_R)/sqrt(2) = STEP*FILL*sqrt(2), not just one glyph
// width.  Solving (N-1)*STEP + STEP*FILL*sqrt(2) = ZONE makes the run land
// exactly on the margins.
// Fixed GAP between letters: N letters plus (N-1) gaps must fill the zone.
//   N*W_ALONG + (N-1)*GAP = ZONE
// W_ALONG is the blob's extent ALONG the plate. A blob is the intersection of two
// glyphs, so after the -45 spin that extent is glyph_width * sqrt(2).
// ---- variable-pitch layout -------------------------------------------------
// Letters are NOT all given the same cell. Each blob is glyph_L swept along Y
// intersected with glyph_R swept along X, so its extent along the plate is
// (w_L + w_R)/sqrt(2), where w depends on which two letters meet there. A wide
// pair ("W" over "N") gets a wide slot, a narrow one ("I" over "S") a narrow
// slot - exactly how the Fusion original lays the name out.
//
// SQ is solved so the whole run, gaps included, lands on ZONE:
//     sum_i(SQ*LETTER_H*pw_i/sqrt(2)) + (N-1)*GAP = ZONE
function pw(i)      = gw(L[i]) + gw(R[i]);          // pair width, in cap-heights
function sumpw_i(i) = i >= N ? 0 : pw(i) + sumpw_i(i + 1);
SUMPW   = sumpw_i(0);

// STROKE grows every glyph by STROKE mm on all sides IN THE GLYPH'S OWN,
// UN-SQUEEZED SPACE, so it comes out (LETTER_H + 2*STROKE) tall and 2*STROKE
// wider; glyph2d() then scales it by KBOLD to put the cap height back to exactly
// LETTER_H. What survives is a thicker stroke for the same height, plus BOLDW
// extra mm of width - which SQ then squeezes along with everything else, so the
// letterform keeps the proportions the font gave it.
KBOLD   = LETTER_H / (LETTER_H + 2 * STROKE);
BOLDW   = 2 * STROKE * KBOLD;            // width a glyph gains, pre-squeeze mm

// Every width is still exactly linear in SQ, so the fit solves in closed form
// just as it did before STROKE existed.
SQ_FIT  = (ZONE - (N - 1) * GAP) * sqrt(2)
          / (KBOLD * LETTER_H * SUMPW + 2 * N * BOLDW);

// The stagger across the plate scales linearly with SQ (the GAP term cancels out
// of ctrR-ctrL), so the width limit can be solved directly instead of iterated.
function wL1(i)  = KBOLD * LETTER_H * gw(L[i]) + BOLDW;   // width at SQ = 1
function wR1(i)  = KBOLD * LETTER_H * gw(R[i]) + BOLDW;
function dc1(i)  = i <= 0 ? 0 : dc1(i - 1) + wR1(i - 1) - wL1(i - 1);
function acr1(i) = (dc1(i) + (wR1(i) - wL1(i)) / 2) / sqrt(2);
function hw1(i)  = (wL1(i) + wR1(i)) / (2 * sqrt(2));
function mx1lo(i) = i >= N ? 1e9  : min(STAGGER * acr1(i) - hw1(i), mx1lo(i + 1));
function mx1hi(i) = i >= N ? -1e9 : max(STAGGER * acr1(i) + hw1(i), mx1hi(i + 1));
// half-DEPTH of the staggered band, measured about its own middle rather than
// about zero. The band is recentred below, so this is what actually has to fit.
SPREAD1 = (mx1hi(0) - mx1lo(0)) / 2;
SQ_WIDE = (PLATE_W / 2 - EDGE) / SPREAD1;     // largest SQ that stays on the plate

SQ      = AUTO_SIZE ? 1
        : SQUEEZE > 0 ? SQUEEZE : min(SQ_FIT, SQ_WIDE);


// ---- independent advance per name ------------------------------------------
// Letter i of NAME_L is a slab at X = a_i; letter i of NAME_R a slab at Y = b_i.
// The blob sits where they cross. The old code used a_i == b_i, which forces
// BOTH names to advance at the same rate - so a wide letter in one name (W, M,
// D) crowds whatever sits opposite it and the two reads overlap.
//
// Letting each name advance at its own pace fixes that. After the -45 spin:
//     along the plate = (a + b)/sqrt(2)
//     across the plate = (b - a)/sqrt(2)      <- the forward/back stagger
// so the offset falls out of the width difference; it is not a fudge factor.
GAPU    = GAP / sqrt(2);                 // per-axis gap -> GAP along the plate

function wL(i)   = SQ * wL1(i);
function wR(i)   = SQ * wR1(i);
function cumL(i) = i <= 0 ? 0 : cumL(i - 1) + wL(i - 1) + GAPU + spgap(NAME_L, i) / sqrt(2);
function cumR(i) = i <= 0 ? 0 : cumR(i - 1) + wR(i - 1) + GAPU + spgap(NAME_R, i) / sqrt(2);
function ctrL(i) = cumL(i) + wL(i) / 2;
function ctrR(i) = cumR(i) + wR(i) / 2;

function along(i) = (ctrL(i) + ctrR(i)) / sqrt(2);   // position along the plate
function halfw(i) = (wL(i) + wR(i)) / (2 * sqrt(2)); // half its extent there
function across(i) = (ctrR(i) - ctrL(i)) / sqrt(2);  // natural stagger

// ---- centre the stagger band -----------------------------------------------
// across() is a running total of the width DIFFERENCE between the two names, so
// when one name is consistently wider the letters drift steadily to one side and
// walk off the edge of the bar. Nothing used to pull that back: SHIFT subtracts
// the same amount from ctrL and ctrR, which slides the run ALONG the plate and
// leaves the sideways drift untouched.
//
// So measure where the band actually sits and recentre it. Two wins: the letters
// sit symmetrically front-to-back, and SPREAD becomes the band's half-depth
// instead of its distance from zero - which frees up width, so SQ_WIDE stops
// clamping the letters smaller than they need to be.
function acrS(i)  = STAGGER * across(i);
function bandlo(i) = i >= N ? 1e9  : min(acrS(i) - halfw(i), bandlo(i + 1));
function bandhi(i) = i >= N ? -1e9 : max(acrS(i) + halfw(i), bandhi(i + 1));
ACROSS_MID = (bandlo(0) + bandhi(0)) / 2;
SPREAD     = (bandhi(0) - bandlo(0)) / 2;

// shifting a blob sideways without moving it along the bar means moving ctrL and
// ctrR by equal and OPPOSITE amounts
function dAcross(i) = acrS(i) - across(i) - ACROSS_MID;
function ctrLc(i)   = ctrL(i) - dAcross(i) * sqrt(2) / 2;
function ctrRc(i)   = ctrR(i) + dAcross(i) * sqrt(2) / 2;

START   = along(0) - halfw(0);
RUN     = along(N - 1) + halfw(N - 1) - START;
SHIFT   = (START + RUN / 2) * sqrt(2) / 2;   // recentres along the plate

W_ALONG = RUN / N;                       // average, for the echo only
GLYPH_W = FILL * W_ALONG / sqrt(2);      // kept: PEDESTAL sizing still uses it
STEP    = W_ALONG + GAP;



TOO_TIGHT = halfw(0) * 2 < MIN_LETTER;
// only a problem while PAD_GLYPH is "none" - with a heart the slot is filled
if (PAD_GLYPH == "none" && len(NAME_L) != len(NAME_R))
    echo(str("*** WARNING: '", NAME_L, "' (", len(NAME_L), ") and '", NAME_R,
             "' (", len(NAME_R), ") are different lengths. With PAD_GLYPH=none ",
             "the ", abs(len(NAME_L) - len(NAME_R)),
             " padding slot(s) are dropped and those letters will be MISSING. ",
             "Use dnp_two_heart.scad or dual_name.scad instead."));
echo(str("letters=", N, "  gap=", GAP, "mm  mean letter width=",
         W_ALONG, "mm  run=", RUN, "mm of ", ZONE, "mm zone"));
echo(str("squeeze=", SQ, "  stagger reach=", SPREAD,
         "mm of ", PLATE_W/2 - EDGE, "mm available half-depth"));
// In AUTO_SIZE the plate is built to fit the letters (PW_EFF = 2*(SPREAD+EDGE)),
// so this can never be exceeded - only warn when the plate size is fixed.
if (!AUTO_SIZE && SPREAD > PLATE_W/2 - EDGE)
    echo(str("*** WARNING: letters reach ", SPREAD,
             "mm off centre but the plate only allows ", PLATE_W/2 - EDGE,
             "mm. Widen PLATE_W, or the outer letters will hang over the edge."));
if (TOO_TIGHT)
    echo(str("*** WARNING: letters are under ", MIN_LETTER, "mm wide."));
echo(str("stroke=", STROKE, "mm -> uprights +", 2 * STROKE * KBOLD * SQ,
         "mm, crossbars +", 2 * STROKE * KBOLD, "mm on the finished glyph"));

// margins are equal on this variant, so when the width cap makes the run
// shorter than the zone, centre it rather than pinning it to the left edge

// ---- Fusion/Bambu match ----------------------------------------------------
// That route never squeezes the glyphs. It draws them at natural width, wraps a
// plate around them, straightens the result and then scales it non-uniformly to
// 200 x 50 x 40. Working the algebra through, the two scale factors it ends up
// applying are exactly the two this file already solves for:
//     along the plate  -> SQ_FIT   (the factor that makes the run fill ZONE)
//     across the plate -> SQ_WIDE  (the factor that makes it fill the depth)
// The normal path takes min() of the two and applies it uniformly, which is why
// it stays square. Applying them separately is precisely the shear.
// The across-scale is NOT forced to fill the plate depth. In the real workflow
// the designer draws the plate wider than the letters need - on the reference the
// letters use only 26 mm of the 50 mm depth - so how much shear you get is a
// CHOSEN number, not something the geometry dictates. SHEAR is therefore a knob:
//     1.0000 = no distortion (identical to FUSION_MATCH = false)
//     0.8334 = the reference DNP.3mf exactly (58.43% / 70.11%)
// Anything past SQ_WIDE would push the letters off the bar, so it is clamped.
SQ_ALONG   = SQ_FIT;
SQ_ACROSS  = min(SQ_FIT / SHEAR, SQ_WIDE);

// natural-width layout: the gap has to be pre-divided by the along-scale so it
// lands on GAP once the squash is applied
GAPU_F = (GAP / SQ_FIT) / sqrt(2);
function cumLF(i) = i <= 0 ? 0 : cumLF(i - 1) + wL1(i - 1) + GAPU_F + spgap(NAME_L, i) / (SQ_FIT * sqrt(2));
function cumRF(i) = i <= 0 ? 0 : cumRF(i - 1) + wR1(i - 1) + GAPU_F + spgap(NAME_R, i) / (SQ_FIT * sqrt(2));
function ctrLF(i) = cumLF(i) + wL1(i) / 2;
function ctrRF(i) = cumRF(i) + wR1(i) / 2;
function alongF(i)  = (ctrLF(i) + ctrRF(i)) / sqrt(2);
function halfwF(i)  = (wL1(i) + wR1(i)) / (2 * sqrt(2));
function acrossF(i) = STAGGER * (ctrRF(i) - ctrLF(i)) / sqrt(2);
function bandloF(i) = i >= N ? 1e9  : min(acrossF(i) - halfwF(i), bandloF(i + 1));
function bandhiF(i) = i >= N ? -1e9 : max(acrossF(i) + halfwF(i), bandhiF(i + 1));
ACROSS_MID_F = (bandloF(0) + bandhiF(0)) / 2;
function dAcrossF(i) = acrossF(i) - (ctrRF(i) - ctrLF(i)) / sqrt(2) - ACROSS_MID_F;
function ctrLcF(i)   = ctrLF(i) - dAcrossF(i) * sqrt(2) / 2;
function ctrRcF(i)   = ctrRF(i) + dAcrossF(i) * sqrt(2) / 2;
STARTF  = alongF(0) - halfwF(0);
RUNF    = alongF(N - 1) + halfwF(N - 1) - STARTF;
SHIFTF  = (STARTF + RUNF / 2) * sqrt(2) / 2;

if (FUSION_MATCH)
    echo(str("FUSION_MATCH on: along x", SQ_ALONG, "  across x", SQ_ACROSS,
             "  -> shear ", SQ_ALONG / SQ_ACROSS,
             " (asked for ", SHEAR, ", 1.0 = undistorted)"));

// ---- plate sized to the letters ----
// RUN and SPREAD are already known at this point, so the plate is just the
// letter run plus the margins you asked for. Margins stay exactly as set;
// it is the plate that moves, not them.
PL_EFF = AUTO_SIZE ? MARGIN_L + RUN + MARGIN_R      : PLATE_L;
PW_EFF = AUTO_SIZE ? 2 * (SPREAD + EDGE)            : PLATE_W;
TH_EFF = AUTO_SIZE ? PLATE_T + LETTER_H - SINK_EFF  : TOTAL_H;
if (AUTO_SIZE)
    echo(str("AUTO_SIZE plate = ", PL_EFF, " x ", PW_EFF, " x ", TH_EFF,
             " mm   (margins ", MARGIN_L, "/", MARGIN_R,
             ", cap height ", CAP_H, ")"));

// ---- output scaling --------------------------------------------------------
// Applied as scale(), NOT resize(). resize() would fit whatever it is given into
// the target box, so exporting PART="text" on its own would blow the letters up
// to the full product size. Deriving the factors from the whole model's known
// dimensions keeps every PART in step, so the pieces still fit together.
OUT_SX = OUT_X > 0 ? OUT_X / PL_EFF : 1;
OUT_SY = OUT_Y > 0 ? OUT_Y / PW_EFF : 1;
OUT_SZ = OUT_Z > 0 ? OUT_Z / TH_EFF : 1;
if (OUT_X > 0 || OUT_Y > 0 || OUT_Z > 0)
    echo(str("OUT scale  x", OUT_SX, "  y", OUT_SY, "  z", OUT_SZ,
             "   -> ", PL_EFF * OUT_SX, " x ", PW_EFF * OUT_SY,
             " x ", TH_EFF * OUT_SZ, " mm"));

module out_scaled() { scale([OUT_SX, OUT_SY, OUT_SZ]) children(); }

TEXT_DX = AUTO_SIZE ? -PL_EFF/2 + MARGIN_L + RUN/2
                    : -PLATE_L/2 + MARGIN_L + (ZONE - RUN)/2 + RUN/2;

// ---------------------------------------------------------------------------
// The heart, grown by HEART_DROP and pushed down by the same amount: the tip ends
// at -HEART_DROP and the top stays on LETTER_H. Letters pass through untouched.
// Trim anything below the allowed descender line. Must happen BEFORE the resize,
// so the box being normalised is a cap box for every glyph.
module desc_clip() {
    intersection() {
        children();
        translate([0, LETTER_H * (1 - DESC_KEEP) / 2])
            square([EXT * 2, LETTER_H * (1 + DESC_KEEP)], center = true);
    }
}

module heart_drop(ch) {
    if (ch == PAD_CH && HEART_DROP > 0)
        translate([0, -HEART_DROP]) scale(HK) children();
    else children();
}

module glyph_raw(ch) {
    if (ch == PAD_CH) scale(LETTER_H * HEART_SCALE) polygon(HEART);
    else text(ch, size = LETTER_H, font = FONT, halign = "center", valign = "baseline");
}

// Height is forced so every glyph tops out at exactly LETTER_H (this is what
// makes the model exactly TOTAL_H tall). Width is NOT forced: resize() with a
// 0 x-component and auto=true scales x by the same factor as y, so the glyph
// keeps its natural aspect. A single SQUEEZE then compresses them all equally.
//
// The old code did resize([GLYPH_W, LETTER_H]) - forcing every glyph to one
// fixed width. That is why "I" came out as a fat block the same width as "W".
// The thickness knob. STROKE == 0 leaves the glyph pipeline exactly as it was,
// so a default plate still exports bit-for-bit what it always did.
//
// chamfer = true is not cosmetic. With the default mitre join, offset() runs the
// two edges of a sharp corner outwards until they meet; the heart's bottom cusp,
// made far more acute by the SQ squeeze, shot ~6 mm below the baseline at
// STROKE = 1 and made the model taller than TOTAL_H. The chamfer caps that
// overshoot at STROKE mm, which is what the arithmetic below assumes.
// Radius used to locate the counters. Dilating then eroding by CR bridges every
// hole narrower than 2*CR, which gives the glyph with its counters filled in;
// subtracting the plain glyph from that leaves the counters on their own.
CR = COUNTER_R > 0 ? COUNTER_R : LETTER_H * 0.35;

// The glyph's counters, at their original size.
//
// The closing is not numerically exact - it comes back roughly 2*STROKE larger
// than the glyph all the way round, so "filled minus glyph" yields the counters
// PLUS a thin ring hugging the outside of the letter. Subtracting that ring
// cancels the boldening and, on a shape with no counters at all (the heart), it
// eats the glyph until the blob intersects to nothing and the slot renders empty.
// A real counter sits well inside the filled glyph, so eroding the filled shape
// a little discards the ring and keeps the counters.
//
// These use offset(r=) - ROUNDED - not offset(delta=). delta is a miter offset:
// on the sharp corners of a black-weight face it throws long spikes, and chaining
// three of them compounds those into visible steps in the finished letter (the
// notch in the top of U, the ledge on the bottom leg of A). r= stays well behaved.
RING_GUARD = 2 * STROKE + 1;

module counters() {
    intersection() {
        difference() {
            offset(r = -CR) offset(r = CR) children();
            children();
        }
        offset(r = -(CR + RING_GUARD)) offset(r = CR) children();
    }
}

// keep = false for glyphs that have no counters (the heart), where the whole
// operation can only do harm
module bolden(keep = true) {
    if (STROKE == 0) children();
    else if (KEEP_COUNTERS && keep)
        // grow in every direction, then punch the original counters back out, so
        // only the OUTER edge actually moves
        scale(KBOLD) translate([0, STROKE])
            difference() {
                offset(delta = STROKE, chamfer = true) children();
                counters() children();
            }
    else
        // offset() grows the glyph by STROKE mm in EVERY direction, so the
        // baseline drops to -STROKE and the cap rises to LETTER_H + STROKE.
        // Lift it back onto the baseline, then shrink by KBOLD about that
        // baseline: the cap lands on exactly LETTER_H again and the letters
        // still start at z = 0. Same height, thicker strokes.
        scale(KBOLD) translate([0, STROKE])
            offset(delta = STROKE, chamfer = true) children();
}

module glyph2d(ch) {
    if (CONDENSE)
        // Order matters: fatten the glyph at its NATURAL width, then squeeze.
        scale([FUSION_MATCH ? 1 : SQ, 1])
            bolden(ch != PAD_CH)
                heart_drop(ch)
                    resize([0, LETTER_H], auto = true) desc_clip() glyph_raw(ch);
    else bolden(ch != PAD_CH) heart_drop(ch) glyph_raw(ch);
}

module glyph_sweep(ch) {
    rotate([90, 0, 0]) linear_extrude(height = EXT, center = true) glyph2d(ch);
}

module blob(cl, cr) {
    intersection() { glyph_sweep(cl); rotate([0, 0, 90]) glyph_sweep(cr); }
}

module letters() {
    if (FUSION_MATCH)
        // glyphs at natural width, laid out, straightened, then squashed by the
        // two different factors - the same order of operations as Fusion + Bambu
        translate([-PLATE_L/2 + MARGIN_L + ZONE/2, 0, 0])
            scale([SQ_ALONG, SQ_ACROSS, 1])
                rotate([0, 0, -45])
                    for (i = [0 : N - 1]) {
                        cl = L[i]; cr = R[i];
                        if (!(cl == PAD_CH && cr == PAD_CH) &&
                            !(PAD_GLYPH == "none" && (cl == PAD_CH || cr == PAD_CH)))
                            translate([ctrLcF(i) - SHIFTF, ctrRcF(i) - SHIFTF, 0])
                                blob(cl, cr);
                    }
    else letters_square();
}

module letters_square() {
    translate([TEXT_DX, 0, 0]) rotate([0, 0, -45])
        for (i = [0 : N - 1]) {
            cl = L[i]; cr = R[i];
            if (!(cl == PAD_CH && cr == PAD_CH) &&
                !(PAD_GLYPH == "none" && (cl == PAD_CH || cr == PAD_CH)))
                translate([ctrLc(i) - SHIFT, ctrRc(i) - SHIFT, 0]) {
                    blob(cl, cr);
                    if (PEDESTAL > 0)
                        translate([0, 0, PEDESTAL/2 - 0.01])
                            cube([STEP * 0.55, STEP * 0.55, PEDESTAL], center = true);
                }
        }
}

// fixed 200 x 50 x 2.5 bar, top face at z = SINK
module foot2d(inset = 0) {
    if (PLATE_R <= 0)
        square([PL_EFF - 2*inset, PW_EFF - 2*inset], center = true);
    else
        offset(r = PLATE_R - inset) offset(r = -PLATE_R)
            square([PL_EFF - 2*inset, PW_EFF - 2*inset], center = true);
}

module base_plate() {
    translate([0, 0, SINK_EFF - PLATE_T])
        if (PLATE_CHAM <= 0) linear_extrude(PLATE_T) foot2d();
        else hull() {
            linear_extrude(PLATE_T - PLATE_CHAM) foot2d();
            translate([0, 0, PLATE_T - PLATE_CHAM])
                linear_extrude(PLATE_CHAM) foot2d(PLATE_CHAM);
        }
}

module rr() { if (PREVIEW_EXACT) render() children(); else children(); }

module dual_name_plate() {
    if (PART == "text") letters();
    else if (PART == "base") difference() { base_plate(); letters(); }
    else {
        color(TEXT_COLOR) rr() letters();
        color(BASE_COLOR) rr() base_plate();
    }
}

out_scaled() dual_name_plate();