// Dual Name Plank - the personalised model, generated per order.
//
// The measurements come from the product's own Fusion sketch (16.DNP/FONTS.png):
// text height 25 mm, a heavy grotesque face (Segoe UI Black on the designer's
// Windows machine; Arial Black is the metric-compatible stand-in available on
// macOS and Linux), hearts trailing the name. Every dimension is a parameter so
// the shop can tune the plank without touching this file - the backend passes
// them with -D.
//
// One caveat worth knowing: Fusion's "character spacing 8.00" is an absolute
// offset, while OpenSCAD's spacing is a multiplier on each glyph's own advance.
// They are not the same number. letter_spacing below is the multiplier that
// reads closest to the sample; it is a parameter precisely so it can be matched
// against a printed plank.

/* [Text] */
name_one = "NAME";          // the first name, always present
name_two = "";              // the second name; empty prints a single-name plank
font = "Arial Black";
letter_spacing = 1.15;      // OpenSCAD advance multiplier, not millimetres

/* [Sizes, mm] */
text_height_mm = 25;        // cap height, from the Fusion sketch
text_thickness_mm = 6;      // how far the letters stand proud of the base
line_gap_mm = 14;           // vertical gap between the two names
base_thickness_mm = 4;      // the plate the letters stand on
base_margin_mm = 12;        // plate margin around the text block
base_depth_mm = 50;         // front-to-back depth of the plate

/* [Preview] */
// The filament the customer chose, as a CSS colour. Only the rendered picture
// uses it - an STL carries no colour - so the preview shows the plank in the
// colour that will actually be loaded.
model_colour = "";

/* [Hearts] */
hearts = 0;                 // how many hearts trail the name
heart_stl = "";             // path to RED HEART.stl; empty draws none
heart_gap_mm = 6;

// A rough advance per character. OpenSCAD cannot measure rendered text, so the
// plate is sized from this estimate - deliberately generous, since a plate a
// few millimetres too wide still prints correctly while one too narrow clips
// the letters or seats a heart on top of them. 0.95 is measured against Arial
// Black, which is wider than most faces; a lighter font simply leaves more
// margin.
function text_width(s) = len(s) * text_height_mm * 0.95 * letter_spacing;

lines = name_two == "" ? 1 : 2;
text_block_w = max(text_width(name_one), text_width(name_two));
heart_size_mm = text_height_mm * 0.72;
heart_w = hearts > 0 ? hearts * (heart_size_mm + heart_gap_mm) + heart_gap_mm : 0;
plate_w = text_block_w + heart_w + 2 * base_margin_mm;
plate_h = lines * text_height_mm + (lines - 1) * line_gap_mm + 2 * base_margin_mm;

module name_line(s, y) {
    translate([base_margin_mm, y, base_thickness_mm])
        linear_extrude(height = text_thickness_mm)
            text(s, size = text_height_mm, font = font, spacing = letter_spacing,
                 halign = "left", valign = "baseline", $fn = 32);
}

module plate() {
    cube([plate_w, plate_h, base_thickness_mm]);
}

module heart_row() {
    if (hearts > 0 && heart_stl != "") {
        for (i = [0 : hearts - 1]) {
            translate([base_margin_mm + text_block_w + heart_gap_mm + i * (heart_size_mm + heart_gap_mm),
                       base_margin_mm,
                       base_thickness_mm])
                // The heart is authored 18 x 18 x 38 mm; scale it to the text.
                scale(text_height_mm / 38) import(heart_stl, convexity = 5);
        }
    }
}

module plank() {
    union() {
        plate();
        name_line(name_one, base_margin_mm);
        if (lines == 2) name_line(name_two, base_margin_mm + text_height_mm + line_gap_mm);
        heart_row();
    }
}

if (model_colour == "") plank();
else color(model_colour) plank();
