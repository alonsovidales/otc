// SPDX-License-Identifier: CC-BY-SA-4.0
// =============================================================================
//  Off The Cloud - screen case: the OTC device on the back of a screen
//  Raspberry Pi 5 + GeeekPi/52Pi "Armor Lite V5" and its two status LEDs,
//  screwed to the back of a portable screen by its four M2.5 holes (a 75 x 75
//  mm square, VESA 75) and hung on the wall by two keyholes. The screen takes
//  the Pi's HDMI by a short cable round its side.
//
//  From the wall: the base (its floor flat on the wall, the two keyholes and
//  "Off The Cloud" in it), the Pi, the lid (its top towards the screen, held
//  3 mm off it by four printed spacers so the cooler's fan can breathe), the
//  screen.
//  Hung with the screen landscape and the case's back up: the part of the case
//  behind the board, where the keyholes are, at the top; the USB-C/HDMI side
//  down (the cables hang, the screen's HDMI port is near); the USB/Ethernet
//  end on the left and the microSD end on the right, seen from behind.
//
//  Two printed parts and four spacers, no supports:
//    base - printed upright (floor on the bed)
//    lid  - printed upside down (top on the bed)
//  One insert and one screw size (M2.5x4 heat-set inserts, M2.5 screws):
//    * lid to the screen: 4 x M2.5x8 from inside the lid, through it and a spacer
//    * back of the Pi: 2 x M2.5x6 from above into inserts in the base standoffs
//    * front of the Pi: 2 x M2.5x8 from the wall side, up through the base
//      standoffs and the Pi's front holes into inserts in lid bosses
//    * back of the case: 2 x M2.5x8 from the wall side, through base pillars
//      into inserts in lid bosses
//
//  Coordinates: as otc-case.scad - origin at the Pi's PCB corner where the
//  USB-C/HDMI edge meets the microSD edge, +X along the 85 mm edge towards the
//  USB/Ethernet end, +Y along the 56 mm edge towards the GPIO header, Z from
//  the wall (Z=0, the base's floor) towards the screen. Hung, up is +Y and,
//  seen from behind, right is -X. All sizes in mm.
//
//  Export: see render.sh, e.g.
//    openscad -D 'part="base_print"' -o otc-screen-case-base.stl otc-screen-case.scad
// =============================================================================

/* [Build] */
// What to produce. *_print parts are in printing orientation (for STL export).
part = "assembly"; // [assembly, on_screen, exploded, base, lid, base_print, lid_print, spacers, test_keyhole, cut_front, cut_keyhole, cut_usb, pi_ref, check_static, check_base_drop, check_lid_drop, check_parts, check_heads]
// Show the reference Pi 5 + cooler in assembly/exploded/cut views (never in STLs)
show_pi = true;

/* [Walls and fits] */
wall        = 2.0;   // side walls
floor_t     = 2.0;   // base floor (on the wall)
top_t       = 2.0;   // lid top (towards the screen)
port_clear  = 0.4;   // per side, around every port cut-out
fit_clear   = 0.2;   // lid lip to base wall
board_gap   = 1.0;   // PCB edge to inner wall on the front and the microSD end
usb_proud   = 0.2;   // USB/Ethernet faces stand this far proud of the end wall
corner_r    = 6;     // the box's corner radius (top view)
soft_R      = 2.5;   // printable rounded edge on the floor and the top
soft_n      = 6;     // slices used for that edge

/* [Heights] */
standoff_h = 4.0;   // floor top to PCB bottom (pins, the cooler's screw heads, the microSD card)
inner_h    = 22.5;  // PCB top to lid ceiling (cooler 15-16 + ~6 mm of air over the fan)

/* [Screen] */
// Its four M2.5 holes: a screen_pitch square, screwed into from inside the lid.
// The square's columns sit centred along the case; its front row clears the
// front-left lid boss, and the case reaches back far enough for the back row.
screen_pitch   = 75;
screen_row0    = 10;    // y of the square's front row (the boss beside it ends at 6.5)
spacer_h       = 3.0;   // printed spacers between lid and screen: the fan's air gap
spacer_d       = 9.0;
// stand-in for the screen in the on_screen view, an example 9.7-inch portable
// screen (ESTIMATED; only that view uses it): size
// (landscape: width along X, height along Y), how far its centre is below the
// hole square's centre, and its HDMI port (on its right edge seen from behind,
// the -X side) below that centre
screen_size    = [238, 185, 9];
screen_c_down  = 29;
screen_hdmi_down = 52;

/* [Raspberry Pi 5 - official mechanical drawing] */
pcb      = [85, 56];
pcb_t    = 1.4;
pcb_r    = 3;
pi_holes = [[3.5, 3.5], [61.5, 3.5], [3.5, 52.5], [61.5, 52.5]];  // 58 x 49, ø2.7
pi_hole_d = 2.7;
cooler_holes = [[3.5, 9.5], [61.5, 46.5]];  // ø3, used by the Armor Lite's 2 screws (from below)
back_gap = 2.0;     // PCB edge to the back region (GPIO side)

// Front edge (y = 0): centres along X; heights above PCB top
usbc_x   = 11.2;   usbc_zc = 1.6;
hdmi_x   = [25.8, 39.2]; hdmi_zc = 1.7;
usbc_open = [12.5, 7.5];
hdmi_open = [11.5, 8.0];
hdmi_combined = true;   // one slot for both micro-HDMI

// USB/Ethernet end (x = 85): centres along Y
port_overhang = 3.0;
eth_y  = 10.2;  eth_w = 15.9;  eth_h = 13.9;
usb3_y = 29.1;  usb2_y = 47.0;
usb_w  = 14.5;  usb_h = 16.0;
port_z0 = 0.65;
usb_port_zc = [4.5, 12.5];  // ESTIMATE (reference model only)

// microSD end (x = 0)
sd_y   = 28.05;  sd_open = [12.5, 2.2];  sd_zc = -2.5;
sd_recess = [14, 8, 1.2];
pwr_y  = 18.4;  pwr_zc = 1.75;  pwr_hole_d = 3.5;
act_y  = 13.3;  act_zc = 0.8;   act_window = [2.0, 3.0];

hdr_c = [32.5, 52.5];   // GPIO header centre

/* [Armor Lite V5 (maker's product-size image + estimates)] */
al_len = 68;  al_left_len = 45;  al_depth = 40;  al_right_depth = 28;
al_body_h = 11;  al_total_h = 15;
al_x0 = 1.25;  al_y_top = 46.8;   // ESTIMATES
fan_c = [27, 24.4];  fan_d = 30;  fan_proud = 1.0;   // ESTIMATES

/* [Status LEDs (3 mm round bi-colour, pushed in from inside)] */
// As otc-case.scad's "gap" layout: two LEDs on the line of the gap between the
// Ethernet jack and the blue USB stack, the top one above the ports in the
// lid, the bottom one below them in the base (under the board).
led_bore       = 3.15;  // through hole for the body
led_seat_d     = 4.0;   // counterbore for the ~3.8 mm flange
led_seat_depth = 1.0;   // counterbore depth from the inner face
led_bar        = 1.0;   // plastic left between a port cut-out and the top LED's bore
led_low_floor  = 0.2;   // the bottom LED's counterbore stays this far above the floor

/* [Base text] */
// Debossed in the base's floor, on the wall side: it reads right from behind,
// with the case's back (+Y) up
text_on    = true;
text_str   = "Off The Cloud";
text_font  = "Liberation Sans:style=Bold";
text_size  = 7.0;
text_depth = 0.7;
text_y     = 30;
// the web address, a smaller line under the name
url_on     = true;
url_str    = "off-the.cloud";
url_size   = 4.5;
url_y      = 22.3;

/* [Screws] */
// One insert and one screw size everywhere: M2.5x4 heat-set inserts and M2.5
// screws, socket or button head: screw_up long for the two from above into
// the back standoffs, screw_down for the rest (the screen, the front Pi holes,
// the case's back).
insert_d      = 3.6;   insert_depth = 5;     // M2.5 x 4 heat-set insert, 1 mm past it for the melt (set insert_d to its maker's hole size)
screw_clear_d = 2.9;   screw_head_d = 5.0;   // M2.5 clearance; pocket for a socket or button head (4.5-4.7 mm)
screw_up      = 6;
screw_down    = 8;
// Pi standoffs: one taper each, standoff_d on the floor (less by the cooler's
// two screw heads under the board: standoff_base_d) to the Pi's 6 mm keep-out
standoff_d     = 7.5;
standoff_top_d = 6.0;
cooler_head_d  = 4.5;   cooler_head_h = 2.0;   // the Armor Lite's screw heads under the board
// Front Pi holes: from the wall side through standoff + PCB into a lid boss;
// the bosses as otc-case.scad's: a pillar_d circle less the room the parts
// beside it need, the same outline all the way, ending thru_gap over the board
thru_tunnel    = 1.5;   // plastic between the head and the PCB
thru_gap       = 1.5;
part_clear     = 0.3;
thru_round     = 1.5;
thru_overrun   = 9;     // a clearance hole this far over the boss's end, for a longer screw
// Case's back: from the wall side through base pillars into lid bosses
pillar_d       = 10.0;
lid_tunnel     = 4.0;   // plastic between the head and the joint: screw_down fills the insert
lid_overrun    = 9;
lid_screws     = [[30, 86], [55, 86]];   // pillars 1 mm into the back wall, between the keyholes
lip_t = 1.2;  lip_h = 3.0;   // alignment lip under the lid

/* [Keyholes (wall screws)] */
// Two keyholes side by side in the floor of the case's back region (behind the
// board: up when hung), either side of the screen's centre line, each in a
// boss on the floor. A wall screw's head goes in the entry and the case drops
// onto it, the shank sliding up the slot (towards +Y), the head in a pocket
// behind the lip (the floor). For screws with heads up to key_head_d across
// and key_pocket_h tall.
key_pitch    = 64;         // between the two wall screws
key_y        = 80;         // y of the slots' top ends, where the screws sit
key_travel   = 7;          // entry centre to slot end
key_head_d   = 9.0;        // entry hole and pocket width
key_shank_w  = 4.5;        // slot width
key_pocket_h = 3.5;        // pocket depth behind the lip
key_cover    = 1.5;        // plastic over the pocket
key_margin   = 3.0;        // plastic round the keyhole in its boss

/* [Vents] */
vent_d = 36;  vent_cell = 4.0;  vent_bar = 1.3;   // round honeycomb over the fan
vent2_c = [60, 33];  vent2_size = [18, 22];       // patch over the heatsink's right fins / RP1
side_slot_w = 2.0;  side_slot_pitch = 4.5;

/* [Exploded] */
explode = 28;

// ----------------------------------------------------------------------------
// Derived levels
// ----------------------------------------------------------------------------
$fn = 48;
eps = 0.01;

z_pb    = floor_t + standoff_h;         // PCB bottom
z_pt    = z_pb + pcb_t;                 // PCB top
z_ceil  = z_pt + inner_h;               // lid ceiling (inside)
z_top   = z_ceil + top_t;               // outside top, towards the screen
z_split = z_pt + max(usbc_zc + usbc_open[1]/2, hdmi_zc + hdmi_open[1]/2);  // base/lid joint

x_end_out   = pcb[0] + port_overhang - usb_proud;   // flat USB end, outer face
x_end_in    = x_end_out - wall;
y_front_out = -board_gap - wall;
x_left_out  = -board_gap - wall;

// the screen's holes, and the case's back face: far enough for the back row's
// spacer to sit on the flat top, and its screw's head inside
screen_c = [(x_left_out + x_end_out) / 2, screen_row0 + screen_pitch / 2];
screen_holes = [for (i = [-1, 1], j = [-1, 1]) [screen_c[0] + i * screen_pitch/2, screen_c[1] + j * screen_pitch/2]];
y_back_out = max(screen_row0 + screen_pitch + screw_head_d/2 + 0.5 + wall,
                 screen_row0 + screen_pitch + spacer_d/2 + soft_R);
// the keyholes' columns, and their bosses' height from the wall
key_xs = [screen_c[0] - key_pitch/2, screen_c[0] + key_pitch/2];
key_t  = floor_t + key_pocket_h + key_cover;

usb_open_top = z_pt + usb_h + port_clear;

// the LEDs: the gap's line between the Ethernet jack and the blue stack; the
// top LED clears the cut-outs its bore overlaps by led_bar, the bottom one
// sits on the floor, under the cut-outs and the board
gap_y = (eth_y + eth_w/2 + usb3_y - usb_w/2) / 2;
port_cut_bottom = z_pt + port_z0 - port_clear;
eth_open_top    = z_pt + port_z0 + eth_h + port_clear;
function bore_overlaps(y, lo, hi) = y + led_bore/2 > lo && y - led_bore/2 < hi;
led_up_z = max(bore_overlaps(gap_y, eth_y - eth_w/2 - port_clear, eth_y + eth_w/2 + port_clear) ? eth_open_top : 0,
               bore_overlaps(gap_y, usb3_y - usb_w/2 - port_clear, usb3_y + usb_w/2 + port_clear) ? usb_open_top : 0)
           + led_bar + led_bore/2;
led_dn_z = floor_t + led_low_floor + led_seat_d/2;
led_positions = [[gap_y, led_up_z], [gap_y, led_dn_z]];

// standoffs: the widest one may be at the height of the cooler's screw heads
// (0.4 mm clear of them), and the foot that keeps every taper that narrow
function head_room_d(h) = 2 * (min([for (c = cooler_holes) norm(h - c)]) - cooler_head_d/2 - 0.4);
standoff_base_d = min(standoff_d, standoff_top_d + (min([for (h = pi_holes) head_room_d(h)]) - standoff_top_d)
                                                   * standoff_h / (cooler_head_h + 0.3));
thru_holes = [pi_holes[0], pi_holes[1]];   // front Pi holes: screws from the wall side into the lid
back_holes = [pi_holes[2], pi_holes[3]];   // back Pi holes: screws from above into the base
thru_head_depth = z_pb - thru_tunnel;
lid_head_depth  = z_split - lid_tunnel;

echo(str("OTC screen case: outside ", x_end_out - x_left_out, " x ", y_back_out - y_front_out, " x ", z_top,
         " mm (+ ", spacer_h, " spacers); split=", z_split, "; screen holes ", screen_holes,
         "; wall screws ", key_pitch, " mm apart, ", key_y - screen_c[1], " above the holes' centre; LEDs at ", led_positions));
echo(str("OTC screen case: screws M2.5x", screw_up, " from above (", (z_pt - screw_up) - (z_pb - insert_depth),
         " short of the hole's end), M2.5x", screw_down, " elsewhere: ", screw_down - top_t - spacer_h,
         " mm into the screen, ", screw_down - thru_tunnel - pcb_t - thru_gap, " into the front inserts, ",
         screw_down - lid_tunnel, " into the back ones; standoffs ", standoff_base_d, " to ", standoff_top_d));

// ----------------------------------------------------------------------------
// Outlines and the soft edge
// ----------------------------------------------------------------------------
module outline2d() {
  translate([x_left_out, y_front_out])
    offset(r = corner_r) offset(delta = -corner_r)
      square([x_end_out - x_left_out, y_back_out - y_front_out]);
}
module cavity2d() { offset(delta = -wall) outline2d(); }

// printable rounded edge: 45 deg chamfer up to the tangent point, then a quarter round
function soft_inset(z) =
  let(R = soft_R, zt = R * (1 - sqrt(2)/2))
  (z <= zt) ? (2*zt - z) :
  (z >= R)  ? 0 :
  R - sqrt(R*R - (R - z)*(R - z));

module soft_body(z0, z1, soft_at_top = false) {
  for (i = [0 : soft_n - 1]) {
    a = soft_R * i / soft_n;  b = soft_R * (i + 1) / soft_n;
    d = soft_inset((a + b) / 2);
    zz = soft_at_top ? z1 - b - (i < soft_n - 1 ? eps : 0) : z0 + a;
    translate([0, 0, zz]) linear_extrude(b - a + (i < soft_n - 1 ? eps : 0)) offset(delta = -d) children();
  }
  translate([0, 0, soft_at_top ? z0 : z0 + soft_R])
    linear_extrude(z1 - z0 - soft_R) children();
}

// ----------------------------------------------------------------------------
// Cutters
// ----------------------------------------------------------------------------
module wall_hole(axis, u, zc, w, h, r, x0, x1) {
  if (axis == "y")
    translate([u, x1, zc]) rotate([90, 0, 0])
      linear_extrude(x1 - x0) offset(r = r) offset(delta = -r) square([w, h], center = true);
  else
    translate([x0, u, zc]) rotate([90, 0, 90])
      linear_extrude(x1 - x0) offset(r = r) offset(delta = -r) square([w, h], center = true);
}

// USB-C / micro-HDMI: U-slots in the base up through the joint; the lid's wall
// closes them
module front_ports_cut() {
  y0 = y_front_out - 1;  y1 = y_front_out + wall + 0.3;
  b = z_pt + usbc_zc - usbc_open[1]/2;
  wall_hole("y", usbc_x, (b + z_split + 2)/2, usbc_open[0], z_split + 2 - b, 1.5, y0, y1);
  hb = z_pt + hdmi_zc - hdmi_open[1]/2;
  if (hdmi_combined) {
    w = hdmi_x[1] - hdmi_x[0] + hdmi_open[0];
    wall_hole("y", (hdmi_x[0] + hdmi_x[1])/2, (hb + z_split + 2)/2, w, z_split + 2 - hb, 1.5, y0, y1);
  } else
    for (x = hdmi_x)
      wall_hole("y", x, (hb + z_split + 2)/2, hdmi_open[0], z_split + 2 - hb, 1.5, y0, y1);
}

// USB/Ethernet end: receptacle-sized openings through the whole end wall
module end_ports_cut() {
  x0 = x_end_in - 0.6;  x1 = x_end_out + 1;
  zb = z_pt + port_z0 - port_clear;
  et = z_pt + port_z0 + eth_h + port_clear;
  for (o = [[eth_y, eth_w, et], [usb3_y, usb_w, usb_open_top], [usb2_y, usb_w, usb_open_top]])
    wall_hole("x", o[0], (zb + o[2])/2, o[1] + 2*port_clear, o[2] - zb, 0.8, x0, x1);
}

// microSD end (up when hung): card slot + finger recess, power-button hole,
// on-board status-LED window
module left_end_cut() {
  x0 = x_left_out - 30;  x1 = x_left_out + wall + 0.4;
  wall_hole("x", sd_y, z_pt + sd_zc, sd_open[0], sd_open[1], 0.6, x0, x1);
  translate([x0, sd_y, z_pt + sd_zc]) rotate([0, 90, 0])
    linear_extrude(x_left_out + sd_recess[2] - x0)
      offset(r = 3) offset(delta = -3) square([sd_recess[1], sd_recess[0]], center = true);
  translate([x0, pwr_y, z_pt + pwr_zc]) rotate([0, 90, 0])
    cylinder(d = pwr_hole_d, h = x1 - x0, $fn = 32);
  translate([x_left_out - eps, pwr_y, z_pt + pwr_zc]) rotate([0, 90, 0])
    cylinder(d1 = pwr_hole_d + 3, d2 = pwr_hole_d, h = 1.5, $fn = 32);
  wall_hole("x", act_y, z_pt + act_zc, act_window[0], act_window[1], 0.9, x0, x1);
}

// a keyhole in column x: the slot + entry through the floor (the lip), the
// pocket for the head behind it; the slot's top end at key_y, the entry
// key_travel below
module keyhole_cut(x) {
  ys = key_y;  ye = key_y - key_travel;
  translate([0, 0, -1]) linear_extrude(floor_t + 1 + eps) {
    translate([x, ye]) circle(d = key_head_d);
    hull() { translate([x, ys]) circle(d = key_shank_w); translate([x, ye]) circle(d = key_shank_w); }
  }
  translate([0, 0, floor_t]) linear_extrude(key_pocket_h)
    hull() { translate([x, ys]) circle(d = key_head_d + 0.5); translate([x, ye]) circle(d = key_head_d + 0.5); }
}
// its boss on the floor
module keyhole_boss2d(x) {
  y0 = key_y - key_travel - key_head_d/2 - key_margin;  y1 = key_y + key_shank_w/2 + key_margin;
  offset(r = 2) offset(delta = -2)
    translate([x - key_head_d/2 - key_margin, y0]) square([key_head_d + 2 * key_margin, y1 - y0]);
}

// a 3 mm LED: bore + flange counterbore from the inside face of the USB end
// wall, a small chamfer outside
module led_hole(y, z) {
  translate([x_end_in - 1, y, z]) rotate([0, 90, 0]) {
    cylinder(d = led_bore, h = wall + 3, $fn = 40);
    cylinder(d = led_seat_d, h = 1 + led_seat_depth, $fn = 40);
  }
  translate([x_end_out - 0.4, y, z]) rotate([0, 90, 0])
    cylinder(d1 = led_bore, d2 = led_bore + 1.2, h = 0.6 + eps, $fn = 40);
}
module leds_cut() { for (p = led_positions) led_hole(p[0], p[1]); }

// "Off The Cloud" and the web address in the floor, mirrored so they read
// right from the wall side
module text_cut() {
  translate([screen_c[0], text_y, -1]) linear_extrude(text_depth + 1)
    mirror([1, 0, 0]) text(text_str, size = text_size, font = text_font, halign = "center", valign = "center", $fn = 24);
  if (url_on)
    translate([screen_c[0], url_y, -1]) linear_extrude(text_depth + 1)
      mirror([1, 0, 0]) text(url_str, size = url_size, font = text_font, halign = "center", valign = "center", $fn = 24);
}

// ----------------------------------------------------------------------------
// Base
// ----------------------------------------------------------------------------
module base_shell() { soft_body(0, z_split, false) outline2d(); }

module base() {
  difference() {
    union() {
      difference() {
        base_shell();
        translate([0, 0, floor_t]) linear_extrude(z_split) cavity2d();
      }
      // Pi standoffs: one taper each, the board resting on the top's keep-out
      for (h = pi_holes) translate([h[0], h[1], floor_t - eps])
        cylinder(d1 = standoff_base_d, d2 = standoff_top_d, h = standoff_h + eps);
      // the case's back pillars, merged into the back wall
      intersection() {
        for (p = lid_screws) translate([p[0], p[1], 0]) cylinder(d = pillar_d, h = z_split);
        base_shell();
      }
      // the keyholes' bosses on the floor: room for the pocket behind the lip
      for (x = key_xs) linear_extrude(key_t) keyhole_boss2d(x);
    }
    // back Pi holes: inserts (screws from above)
    for (h = back_holes)
      translate([h[0], h[1], z_pb - insert_depth]) cylinder(d = insert_d, h = insert_depth + 1);
    // front Pi holes: through hole + head pocket from the wall side
    for (h = thru_holes) {
      translate([h[0], h[1], -1]) cylinder(d = screw_clear_d, h = z_pb + 2);
      translate([h[0], h[1], -1]) cylinder(d = screw_head_d, h = thru_head_depth + 1);
    }
    // the case's back screws: clearance + head pocket from the wall side
    for (p = lid_screws) {
      translate([p[0], p[1], -1]) cylinder(d = screw_clear_d, h = z_split + 2);
      translate([p[0], p[1], -1]) cylinder(d = screw_head_d, h = lid_head_depth + 1);
    }
    for (x = key_xs) keyhole_cut(x);
    front_ports_cut();
    end_ports_cut();
    left_end_cut();
    leds_cut();
    if (text_on) text_cut();
  }
}

// ----------------------------------------------------------------------------
// Lid
// ----------------------------------------------------------------------------
module lid_shell() { soft_body(z_split, z_top, true) outline2d(); }

// alignment lip: hangs inside the base wall where there are no ports or holes
module lip_region2d() {
  difference() {
    translate([-100, -100]) square([300, 300]);
    translate([4, -100]) square([54, 100 + 4]);                // front: the USB-C/HDMI openings
    translate([78, -100]) square([100, 100 + 58]);             // USB end: the ports
    translate([-100, -100]) square([100 + 4, 100 + 40]);       // microSD end: card, button, LED window
    for (p = lid_screws) translate(p) circle(d = pillar_d + 2 * fit_clear + 0.6);
  }
}

// a front lid boss's outline round the Pi's front hole h: a pillar_d circle
// less every part of the board beside it (part_clear larger), whatever its
// height - one clean block from the ceiling down, corners rounded
module thru_boss2d(h) {
  offset(r = thru_round) offset(delta = -thru_round) difference() {
    translate(h) circle(d = pillar_d);
    projection() pi5_ref(0, false, false, 0, part_clear);
  }
}
module thru_boss(h) {
  intersection() {
    translate([0, 0, z_pt + thru_gap]) linear_extrude(z_ceil - z_pt) thru_boss2d(h);
    union() {
      translate([0, 0, z_pt]) linear_extrude(z_split - z_pt + eps) offset(delta = -fit_clear) cavity2d();
      lid_shell();
    }
  }
}
module thru_boss_hole(h) {
  translate([h[0], h[1], z_pt + thru_gap - 1]) {
    cylinder(d = insert_d, h = insert_depth + 1);
    cylinder(d = screw_clear_d, h = thru_overrun + 1);
  }
}

module honeycomb2d(w, h) {
  pitch = vent_cell + vent_bar;
  r = vent_cell / sqrt(3);
  for (i = [-ceil(w/pitch) - 1 : ceil(w/pitch) + 1], j = [-ceil(h/(pitch*0.866)) - 1 : ceil(h/(pitch*0.866)) + 1])
    translate([i * pitch + (j % 2 == 0 ? 0 : pitch/2), j * pitch * sqrt(3)/2])
      rotate(30) circle(r = r, $fn = 6);
}

// the fan's intake, towards the screen (air comes in through the spacers' gap)
module top_vents_cut() {
  translate([0, 0, z_ceil - 1]) linear_extrude(top_t + 2) {
    translate(fan_c) intersection() { circle(d = vent_d, $fn = 96); honeycomb2d(vent_d, vent_d); }
    translate(vent2_c) intersection() {
      offset(r = 3) offset(delta = -3) square(vent2_size, center = true);
      honeycomb2d(vent2_size[0], vent2_size[1]);
    }
  }
}

// vertical slots through the lid walls: the front above the HDMI/USB-C (not
// where a boss meets it), the back, and the microSD end - up when hung, where
// the warm air the heatsink blows out of its left end leaves
module side_vents_cut() {
  z0 = z_split + 3.5;  z1 = z_ceil - 2.5;
  module slot(len) hull() {
    translate([0, 0, 1]) rotate([0, 90, 0]) cylinder(d = side_slot_w, h = len, $fn = 16);
    translate([0, 0, z1 - z0 - 1]) rotate([0, 90, 0]) cylinder(d = side_slot_w, h = len, $fn = 16);
  }
  for (x = [6 : side_slot_pitch : 80]) if (min([for (h = thru_holes) abs(x - h[0])]) > pillar_d/2 + side_slot_w/2 + 0.5)
    translate([x, y_front_out - 1, z0]) rotate(90) slot(wall + 2);
  for (x = [8 : side_slot_pitch : 78]) if (min([for (p = lid_screws) abs(x - p[0])]) > pillar_d/2 + side_slot_w/2 + 0.5)
    translate([x, y_back_out - wall - 1, z0]) rotate(90) slot(wall + 2);
  for (y = [10 : side_slot_pitch : y_back_out - 8])
    translate([x_left_out - 1, y, z0]) slot(wall + 2);
}

module lid() {
  difference() {
    union() {
      difference() {
        lid_shell();
        translate([0, 0, z_split - 1]) linear_extrude(z_ceil - z_split + 1) cavity2d();
      }
      // lip below the joint + a short ledge above it that ties it to the wall
      intersection() {
        union() {
          translate([0, 0, z_split - lip_h]) linear_extrude(lip_h + eps)
            difference() { offset(delta = -fit_clear) cavity2d(); offset(delta = -fit_clear - lip_t) cavity2d(); }
          translate([0, 0, z_split]) linear_extrude(2)
            difference() { cavity2d(); offset(delta = -fit_clear - lip_t) cavity2d(); }
        }
        translate([0, 0, z_split - lip_h - 1]) linear_extrude(lip_h + 4) lip_region2d();
      }
      // the case's back bosses, from the ceiling down to the joint
      intersection() {
        for (p = lid_screws) translate([p[0], p[1], z_split]) cylinder(d = pillar_d, h = z_ceil - z_split + 0.5);
        lid_shell();
      }
      // bosses down to the Pi's front holes
      for (h = thru_holes) thru_boss(h);
    }
    // inserts: the case's back bosses (from the joint up) and the front ones
    for (p = lid_screws) translate([p[0], p[1], z_split - 1]) {
      cylinder(d = insert_d, h = insert_depth + 1);
      cylinder(d = screw_clear_d, h = lid_overrun + 1);
    }
    for (h = thru_holes) thru_boss_hole(h);
    // the screen's screws: through the top, heads on the ceiling
    for (p = screen_holes) translate([p[0], p[1], z_ceil - 1]) cylinder(d = screw_clear_d, h = top_t + 2);
    end_ports_cut();
    leds_cut();
    top_vents_cut();
    side_vents_cut();
  }
}

// the spacers between the lid and the screen, as printed (flat)
module spacers() {
  for (i = [0 : 3]) translate([i * (spacer_d + 3), 0, 0])
    difference() { cylinder(d = spacer_d, h = spacer_h); translate([0, 0, -1]) cylinder(d = screw_clear_d, h = spacer_h + 2); }
}

// test piece for the keyholes: one keyhole's boss with the floor under it, as
// printed - hang it on your wall screw before printing the base
module test_keyhole() {
  x = key_xs[0];
  translate([-(x - key_head_d/2 - key_margin), -(key_y - key_travel - key_head_d/2 - key_margin), 0])
    difference() { linear_extrude(key_t) keyhole_boss2d(x); keyhole_cut(x); }
}

// ----------------------------------------------------------------------------
// Reference Raspberry Pi 5 + Armor Lite V5 (renders only, never exported)
// ----------------------------------------------------------------------------
module rrect2d(s, r) { offset(r = r) offset(delta = -r) square(s); }

module armor_footprint2d() {
  translate([al_x0, al_y_top - al_depth]) square([al_left_len, al_depth]);
  translate([al_x0 + al_left_len - eps, al_y_top - al_right_depth]) square([al_len - al_left_len, al_right_depth]);
}

// su / sd > 0 extend every part up / down (to test that the board drops into
// the base, and the lid down onto the board, without hitting anything); g > 0
// grows the parts by that much all round (the room the front lid bosses leave)
module pi5_ref(su = 0, board = true, extras = true, sd = 0, g = 0) {
  module blk(p, sz) translate([p[0] - g, p[1] - g, p[2] - sd - g]) cube([sz[0] + 2*g, sz[1] + 2*g, sz[2] + su + sd + 2*g]);
  module cyl(p, d, h) translate([p[0], p[1], p[2] - sd - g]) cylinder(d = d + 2*g, h = h + su + sd + 2*g);
  translate([0, 0, z_pb]) {
    if (board) color([0.05, 0.45, 0.2]) translate([0, 0, -sd]) linear_extrude(pcb_t + su + sd) difference() {
      rrect2d(pcb, pcb_r);
      for (h = pi_holes) translate(h) circle(d = pi_hole_d);
      for (h = cooler_holes) translate(h) circle(d = 3);
    }
    t = pcb_t;
    color("silver") blk([usbc_x - 4.45, -1.3, t + usbc_zc - 1.6], [8.9, 7.3, 3.2]);
    for (x = hdmi_x) color("silver") blk([x - 3.3, -1.5, t + hdmi_zc - 1.7], [6.6, 7.5, 3.4]);
    e = pcb[0] + port_overhang;
    color([0.75, 0.75, 0.75]) blk([e - 21.2, eth_y - eth_w/2, t + port_z0], [21.2, eth_w, eth_h]);
    for (st = [[usb3_y, [0.1, 0.3, 0.95]], [usb2_y, [0.1, 0.1, 0.1]]]) {
      color([0.8, 0.8, 0.8]) blk([e - 17.2, st[0] - usb_w/2, t + port_z0], [17.2, usb_w, usb_h - port_z0]);
      for (zc = usb_port_zc) color(st[1]) translate([e - 0.2, st[0] - 6.2, t + zc - 2.4]) cube([0.4, 12.4, 4.8]);
    }
    color("black") blk([hdr_c[0] - 25.4, hdr_c[1] - 2.54, t], [50.8, 5.08, 2.5]);
    color("gold") for (i = [0:19], j = [0:1])
      blk([hdr_c[0] - 24.13 + i * 2.54 - 0.32, hdr_c[1] - 1.27 + j * 2.54 - 0.32, t], [0.64, 0.64, 8.5]);
    color("white") blk([65.4, 40.4, t], [4.3, 7.3, 5]);
    color("black") blk([59.0, 7.0, t], [5.1, 5.0, 2.5]);
    color("gold") for (i = [0:1], j = [0:1])   // the PoE header's 2 x 2 pins, as tall as the GPIO's
      blk([59.0 + 1.28 + i * 2.54 - 0.32, 7.0 + 1.23 + j * 2.54 - 0.32, t], [0.64, 0.64, 8.5]);
    color("black") for (x = [47.3, 53.5]) blk([x, 0.5, t], [3.0, 16, 4.1]);
    color("white") blk([-0.45, pwr_y - 1.5, t + pwr_zc - 1.15], [3, 3, 2.3]);
    color("lime") blk([0, act_y - 0.8, t + act_zc - 0.5], [1.2, 1.6, 1]);
    color([0.12, 0.12, 0.14]) {
      for (p = cooler_holes) cyl([p[0], p[1], t], 5, al_total_h - al_body_h + eps);
      translate([0, 0, t + al_total_h - al_body_h - sd - g]) linear_extrude(al_body_h + su + sd + 2*g) offset(delta = g) difference() {
        armor_footprint2d();
        translate(fan_c) circle(d = fan_d + 1);
      }
    }
    color([0.3, 0.3, 0.32]) cyl([fan_c[0], fan_c[1], t + al_total_h + fan_proud - 10], fan_d, 10);
    if (extras) {
      color([0.25, 0.25, 0.3]) translate([-2.2, sd_y - 5.5, -1.6]) cube([14, 11, 0.8]);
      color("silver") for (p = cooler_holes) translate([p[0], p[1], -cooler_head_h]) cylinder(d = cooler_head_d, h = cooler_head_h);
    }
  }
  if (extras) {
    color("silver") for (h = back_holes) translate([h[0], h[1], z_pt]) cylinder(d = 4.5, h = 1.8);
    for (p = led_positions) led_ref(p[0], p[1]);
  }
}

// a 3 mm LED, pushed in from inside (flange in the counterbore), leads back
module led_ref(y, z) {
  fx = x_end_in + led_seat_depth;   // flange's front face
  translate([0, y, z]) {
    color([1, 0.2, 0.15, 0.85]) translate([fx - 1, 0, 0]) rotate([0, 90, 0]) cylinder(d = 3.8, h = 1, $fn = 32);
    color([1, 0.2, 0.15, 0.85]) translate([fx, 0, 0]) rotate([0, 90, 0]) cylinder(d = 3.0, h = 3.8, $fn = 32);
    color([1, 0.2, 0.15, 0.85]) translate([fx + 3.8, 0, 0]) sphere(d = 3.0, $fn = 32);
    color("silver") for (dy = [-1.27, 0, 1.27]) translate([fx - 9, dy - 0.2, -0.2]) cube([8, 0.4, 0.4]);
  }
}

// the screen's four screws' heads, on the ceiling
module screen_heads() {
  color("silver") for (p = screen_holes) translate([p[0], p[1], z_ceil - 1.8]) cylinder(d = 4.7, h = 1.8);
}

// stand-in for the screen (ESTIMATED size and position), its HDMI port marked
module screen_ref() {
  c = [screen_c[0], screen_c[1] - screen_c_down];
  z = z_top + spacer_h;
  color([0.45, 0.47, 0.5]) difference() {
    translate([c[0] - screen_size[0]/2, c[1] - screen_size[1]/2, z])
      linear_extrude(screen_size[2]) rrect2d([screen_size[0], screen_size[1]], 8);
    for (p = screen_holes) translate([p[0], p[1], z - 1]) cylinder(d = 2.5, h = 4);
  }
  color("black") translate([c[0] - screen_size[0]/2 - 0.5, screen_c[1] - screen_hdmi_down - 6, z + 2]) cube([1, 12, 4]);
  color([0.9, 0.9, 0.9]) for (p = screen_holes) translate([p[0], p[1], z_top])
    difference() { cylinder(d = spacer_d, h = spacer_h); translate([0, 0, -1]) cylinder(d = screw_clear_d, h = spacer_h + 2); }
}

// ----------------------------------------------------------------------------
// Part selection
// ----------------------------------------------------------------------------
module base_col() { color([0.93, 0.94, 0.96]) base(); }
module lid_col() { color([0.80, 0.86, 0.96]) lid(); }
cut_col = [0.95, 0.55, 0.2];
pi_cut_col = [0.35, 0.4, 0.45];

if (part == "base" || part == "base_print") base();
else if (part == "lid") lid();
else if (part == "lid_print") translate([0, 0, z_top]) rotate([180, 0, 0]) lid();
else if (part == "spacers") spacers();
else if (part == "test_keyhole") test_keyhole();
else if (part == "pi_ref") pi5_ref();
// interference checks: each must render empty or as zero-thickness contacts
else if (part == "check_static") intersection() { union() { base(); lid(); } pi5_ref(); }
else if (part == "check_base_drop") intersection() { base(); pi5_ref(40, true, false); }
else if (part == "check_parts") intersection() { base(); lid(); }
else if (part == "check_lid_drop") intersection() { lid(); pi5_ref(0, false, false, 40); }
else if (part == "check_heads") intersection() { union() { base(); lid(); pi5_ref(); } screen_heads(); }
else if (part == "assembly") {
  base_col(); lid_col();
  if (show_pi) { pi5_ref(); screen_heads(); }
}
else if (part == "on_screen") {
  base_col(); lid_col(); screen_ref();
}
else if (part == "exploded") {
  base_col();
  if (show_pi) translate([0, 0, explode]) pi5_ref();
  translate([0, 0, 2.2 * explode]) { lid_col(); screen_heads(); }
}
else if (part == "cut_front") {
  // section through the two front Pi holes (plane y = 3.5), seen from inside
  intersection() { union() { base_col(); lid_col(); } color(cut_col) translate([-50, -50, -10]) cube([200, 50 + 3.5, 100]); }
  if (show_pi) intersection() { pi5_ref(); color(pi_cut_col) translate([-50, -50, -10]) cube([200, 50 + 3.5, 100]); }
}
else if (part == "cut_keyhole") {
  // section through a keyhole (plane x = its column), seen from +X
  intersection() { base_col(); color(cut_col) translate([key_xs[0], -50, -10]) cube([100, 200, 100]); }
}
else if (part == "cut_usb") {
  // section through the LEDs (plane y = their line), seen from -Y
  intersection() { union() { base_col(); lid_col(); } color(cut_col) translate([-50, gap_y, -10]) cube([200, 200, 100]); }
  if (show_pi) intersection() { pi5_ref(); color(pi_cut_col) translate([-50, gap_y, -10]) cube([200, 200, 100]); }
}
