// =============================================================
//  Moon Lamp in 3 sizes (4 / 5 / 6 inch), light inside, glued bottom cap
//  Inner ball dia 100 / 125 / 150 mm = the reference 3dp.rocks moons
//  (25.MOON LAMP\*\settings-spherical-*.txt), wall 0.6 - 2.6 mm lithophane.
//
//  part = "top"    ball above the cut, prints standing on the cut ring
//         "cap"    bottom cap (moon textured too) with glue spigot, key,
//                  wire hole, flat seat; prints seat down. The old lamp's
//                  neck cylinder is gone: the light goes in through the cut.
//         "stand"  the reference L bracket lying flat (print 2)
//         "stand_groove"  same L with a wire groove underneath
//
//  The moon / photo / text come from skin.stl, built by Tensor.
//  Adapted from the shop's moon_lamp_sizes.scad: only the skin source and
//  the stand changed; every joint, cap and size number is the original.
// =============================================================

size_in  = 4;            // 4, 5 or 6
part     = "top";

// ---------- SIZE ----------
R = size_in == 6 ? 75 : size_in == 5 ? 62.5 : 50;   // inner radius

// ---------- JOINT (same on every size) ----------
open_r   = 30;     // inner radius at the cut -> 60 mm opening
cut_z    = -sqrt(R * R - open_r * open_r);
fit      = 0.2;    // radial clearance spigot / collar
collar_w = 1.6;    // ring inside the top part, above the cut
collar_h = 4;
spig_w   = 1.4;    // ring on the cap that slides into the collar
spig_h   = 3.5;
ledge_h  = 2;      // ring on the cap below the cut (glue face for the collar)
key_w    = 4;      // alignment key at the back (lon 180, opposite the photo)
key_lon  = 180;

r_collar_in = open_r - collar_w;            // 28.4
r_spig_out  = r_collar_in - fit;            // 28.2
r_spig_in   = r_spig_out - spig_w;          // 26.8

// ---------- SEAT + WIRE HOLE (+ optional light post) ----------
light_post = false; // true = Ø22 post in the cap to wrap the LED strip round
seat_drop = 1.0;   // flat seat this far below the inner pole -> ~24 mm flat
seat_r    = 12;
wire_r    = 3.5;   // wire hole out of the bottom (Ø7)
post_r    = 11;    // strip wraps round this (Ø22)
post_in_r = 4.5;   // wire channel inside the post (Ø9)
post_h    = R - 10;          // top of the post 10 mm below the ball centre
flange_r  = 13;    // lip at the top so the strip can't slide off
slot_w    = 7;     // wire exit slot at the foot of the post
slot_h    = 8;

sphere_fn = 240;
BIG = 2 * R + 40;

// ---------- MOON SKIN ----------
// Tensor builds the textured ball itself (internal/moonlamp, SkinSTL) and
// stages it beside this file as skin.stl - a closed mesh of the same surface
// the shop's tool built here as a polyhedron from lamp_map.json, without
// OpenSCAD having to parse a 1-2 MB JSON map first.
module skin() import("skin.stl", convexity = 4);

module above(z) translate([-BIG / 2, -BIG / 2, z]) cube(BIG);
module below(z) translate([-BIG / 2, -BIG / 2, z - BIG]) cube(BIG);
module ring(r1, r2, z0, z1) translate([0, 0, z0]) difference() {
    cylinder(r = r2, h = z1 - z0, $fn = 180);
    translate([0, 0, -1]) cylinder(r = r1, h = z1 - z0 + 2, $fn = 180);
}
module key_box(w, r0, r1, z0, z1)
    rotate([0, 0, key_lon]) translate([r0, -w / 2, z0]) cube([r1 - r0, w, z1 - z0]);

// ---------- TOP ----------
module top() {
    difference() { intersection() { skin(); above(cut_z); } sphere(r = R, $fn = sphere_fn); }
    // collar: fills from the inner surface in to r_collar_in, notch for the key
    difference() {
        intersection() { ring(r_collar_in, R, cut_z, cut_z + collar_h); sphere(r = R + 0.3, $fn = sphere_fn); }
        key_box(key_w, r_collar_in - 1, R, cut_z - 1, cut_z + collar_h + 1);
    }
}

// ---------- CAP ----------
module cap() {
    seat_z = -(R + seat_drop);
    difference() {
        union() {
            difference() {
                union() {
                    intersection() { skin(); below(cut_z); }
                    translate([0, 0, seat_z - 1]) cylinder(r = seat_r, h = 6, $fn = 120);
                }
                sphere(r = R, $fn = sphere_fn);
            }
            // ledge under the collar + spigot above the cut, kept inside the skin
            intersection() {
                union() {
                    ring(r_spig_in, R, cut_z - ledge_h, cut_z);
                    ring(r_spig_in, r_spig_out, cut_z - 0.01, cut_z + spig_h);
                    key_box(key_w - 2 * fit, r_spig_out - 0.5, open_r - fit, cut_z - 0.01, cut_z + spig_h - 0.5);
                }
                union() { sphere(r = R + 0.3, $fn = sphere_fn); above(cut_z + 0.01); }   // +0.01: no zero-thick rim at the cut
            }
            if (light_post) {
                translate([0, 0, seat_z]) cylinder(r = post_r, h = R + seat_drop - (R - post_h) - 1.2, $fn = 90);
                translate([0, 0, -R + post_h - 1.2]) cylinder(r = flange_r, h = 1.2, $fn = 90);
            }
        }
        below(seat_z);                                                       // flat seat
        translate([0, 0, seat_z - 1]) cylinder(r = light_post ? post_in_r : wire_r, h = light_post ? R + 20 : 10, $fn = 48);
        // wire exit slot at the foot of the post (towards the key / back)
        if (light_post)
            rotate([0, 0, key_lon]) translate([0, -slot_w / 2, -sqrt(R * R - post_r * post_r) + 1.5]) cube([post_r + 2, slot_w, slot_h]);
    }
}

// The stand L brackets never change; Tensor ships them pre-rendered.

if (part == "top") top();
else if (part == "cap") cap();
else if (part == "assembly") { top(); cap(); }
