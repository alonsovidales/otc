// SPDX-License-Identifier: CC-BY-SA-4.0
// =============================================================================
//  Off The Cloud - cloud-shaped case for the OTC device
//  Raspberry Pi 5 + GeeekPi/52Pi "Armor Lite V5" heatsink-fan, two mirrored
//  USB disks in the blue USB 3.0 ports, two bi-colour 3 mm status LEDs on the
//  line of the gap between the Ethernet jack and the blue USB stack (one above
//  the ports in the lid, one below them in the base).
//
//  Two printed parts, no supports:
//    base - printed upright (floor on the bed)
//    lid  - printed upside down (top on the bed)
//  Fastening (no screw heads on top):
//    * back of the Pi: 2 x M2.5x6 from above into M2.5 heat-set inserts in the
//      base standoffs
//    * front of the Pi: 2 x M2.5x8 from below, up through the base standoffs
//      and the Pi's front holes into M2.5 heat-set inserts in lid bosses (hold
//      the Pi and the lid)
//    * back of the lid: 2 x M2.5x8 from below, through pillars in the cloud's
//      top lobes into M2.5 heat-set inserts in the lid bosses
//    * all six inserts the same (M2.5x4), all screws M2.5 (x6 above, x8 below)
//
//  Coordinates: origin at the Pi's PCB corner where the USB-C/HDMI edge meets
//  the microSD edge. +X runs along the 85 mm edge towards the USB/Ethernet end,
//  +Y along the 56 mm edge towards the GPIO header, Z=0 is the bottom of the
//  case. All sizes in mm. Sources of every Pi/cooler number: see README.md.
//
//  Export: see render.sh, e.g.
//    openscad -D 'part="base_print"' -o otc-case-base.stl otc-case.scad
// =============================================================================

/* [Build] */
// What to produce. *_print parts are in printing orientation (for STL export).
part = "assembly"; // [assembly, exploded, base, lid, base_print, lid_print, test_usb_wall, test_usb_wall_print, test_inserts, cut_usb, cut_front, cut_side, outline, pi_ref, check_static, check_base_drop, check_lid_drop, check_parts]
// Show the reference Pi 5 + cooler + LEDs in assembly/exploded/cut views (never in STLs)
show_pi = true;
// Lid transparency in assembly views (1 = opaque)
lid_alpha = 1.0;

/* [Walls and fits] */
wall        = 2.0;   // side walls
floor_t     = 2.0;   // base floor
top_t       = 2.0;   // lid top
port_clear  = 0.4;   // per side, around every port cut-out
fit_clear   = 0.2;   // lid lip to base wall
board_gap   = 1.0;   // PCB edge to inner wall on the flat sides (front, microSD end)
back_gap    = 2.0;   // PCB edge to inner wall on the GPIO side (mostly hidden by lobes)
usb_proud   = 0.2;   // USB/Ethernet faces stand this far proud of the end wall
soft_R      = 2.5;   // printable rounded edge at the bottom (base) and top (lid)
soft_n      = 6;     // slices used for that edge

/* [Cloud outline (top view)] */
// Lobes [x, y, r] unioned with the box around the board. The front (y<0) and
// the USB end (x>85) stay flat for the ports. On the microSD end the lid's
// big lobe overhangs a recessed "port bay" in the base (see bay_y).
lobes = [
  [17, 29, 23],   // L  round left end (lid only: it overhangs the port bay by 3 mm)
  [20, 58, 11],   // T1 small top-left bump (back-left lid screw inside)
  [44, 60, 15],   // T2 big top bump
  [70, 57, 12]    // T3 top-right bump (back-right lid screw inside)
];
box_r = [6, 2, 8, 8];   // corner radii of the box round the board: front-left, front-right, back-right, back-left
cusp_fillet  = 7;     // rounds the valleys between lobes
corner_round = 2;     // rounds the remaining convex corners
bay_y = [2, 80];       // port bay on the microSD end: the base's wall stays flat
                      // here (card slot, power button, status-LED window) and
                      // the lid's left lobe overhangs it
bay_r = 2.5;          // inner corner radius of the bay

/* [Heights] */
standoff_h = 4.0;   // floor top to PCB bottom (pins, the cooler's screw heads, the microSD card)
inner_h    = 22.5;  // PCB top to lid ceiling (cooler 15-16 + ~6 mm air over the fan; DuPont plugs)

/* [Raspberry Pi 5 - official mechanical drawing] */
pcb      = [85, 56];
pcb_t    = 1.4;     // measured off the drawing's side view (~1.4)
pcb_r    = 3;
pi_holes = [[3.5, 3.5], [61.5, 3.5], [3.5, 52.5], [61.5, 52.5]];  // 58 x 49, ø2.7
pi_hole_d = 2.7;
cooler_holes = [[3.5, 9.5], [61.5, 46.5]];  // ø3, used by the Armor Lite's 2 screws (from below)
pins_below = 2.0;   // through-hole pins under the USB/Ethernet end (drawing side view)

// Front edge (y = 0): centres along X from the drawing; heights above PCB top
usbc_x   = 11.2;   usbc_zc = 1.6;        // receptacle top 3.2 above PCB (drawing)
hdmi_x   = [25.8, 39.2]; hdmi_zc = 1.7;  // receptacle top 3.4 above PCB (drawing)
// Plug overmould openings [width, height] (typical cables + clearance)
usbc_open = [12.5, 7.5];
hdmi_open = [11.5, 8.0];
hdmi_combined = true;   // one slot for both micro-HDMI

// USB/Ethernet end (x = 85): centres along Y from the drawing
port_overhang = 3.0;    // drawing: ports stand 3 mm past the PCB edge
eth_y  = 10.2;  eth_w = 15.9;  eth_h = 13.9;   // width from drawing top view, height from side view
usb3_y = 29.1;          // BLUE USB 3.0 stack (between Ethernet and the black stack)
usb2_y = 47.0;          // black USB 2.0 stack (GPIO side)
usb_w  = 14.5;  usb_h = 16.0;                  // stack width/height (measured off the drawing)
port_z0 = 0.65;         // receptacle bottoms above PCB top (drawing side view)
usb_port_zc = [4.5, 12.5];  // ESTIMATE: lower/upper port centres above PCB (reference model only)

// microSD end (x = 0)
sd_y   = 28.05;  sd_open = [12.5, 2.2];  sd_zc = -2.5;  // card under the PCB; zc rel. PCB top
sd_recess = [14, 8, 1.2];   // finger recess round the slot [w, h, depth]
pwr_y  = 18.4;  pwr_zc = 1.75;  pwr_hole_d = 3.5;       // power button (drawing: 18.4, 0.45 proud)
act_y  = 13.3;  act_zc = 0.8;   act_window = [2.0, 3.0]; // on-board status LED window

// GPIO header (2 x 20, centre 29 mm right of the hole line)
hdr_c = [32.5, 52.5];

/* [Armor Lite V5 (maker's product-size image + estimates)] */
al_len      = 68;    // 45 + 23 (maker)
al_left_len = 45;    // maker
al_depth    = 40;    // maker
al_right_depth = 28; // maker
al_body_h   = 11;    // maker
al_total_h  = 15;    // maker ("15mm" incl. feet), above PCB top
al_x0       = 1.25;  // ESTIMATE: left end on the board (posts on the cooler holes)
al_y_top    = 46.8;  // ESTIMATE: GPIO-side edge on the board
fan_c       = [27, 24.4];  // ESTIMATE: fan centre on the board
fan_d       = 30;    // ESTIMATE (sold as a "3510" fan; Amazon: 30 mm)
fan_proud   = 1.0;   // ESTIMATE: fan above the heatsink top

/* [Status LEDs (bi-colour, pushed in from inside)] */
// gap:        two 3 mm round LEDs on the line of the gap between the Ethernet
//             jack and the blue USB stack: the top one above the ports, in the
//             lid; the bottom one below them, in the base (under the board).
// between:    two 2 x 5 mm rectangular LEDs standing upright in that gap, each
//             beside its port (a 3 mm LED doesn't fit in it: the gap is 3.7 mm).
// both_above: two 3 mm LEDs above the blue stack, side by side, in the lid.
led_layout     = "gap"; // [gap, between, both_above]
led_rect       = [2.0, 5.0]; // between: LED body across x up (2 x 5 mm rectangular)
led_rect_clear = 0.1;   // between: per side, round the rectangular body
gap_clear      = 0.15;  // between: port clearance on the two sides facing the LED gap (keeps the bars beside the LEDs)
led_bore       = 3.15;  // 3 mm LEDs: through hole for the body
led_seat_d     = 4.0;   // 3 mm LEDs: counterbore for the ~3.8 mm flange
led_seat_depth = 1.0;   // 3 mm LEDs: counterbore depth from the inner face
led_bar        = 1.0;   // 3 mm LEDs: plastic left between a port cut-out and an LED bore
led_low_floor  = 0.2;   // gap: the bottom LED's counterbore stays this far above the floor
led_pair_pitch = 6.0;   // both_above spacing

/* [Screws] */
// One insert and one screw size everywhere: M2.5x4 heat-set inserts and M2.5
// screws, socket or button head (the Pi's ø2.7 holes take nothing bigger):
// screw_up long for the two from above into the back standoffs, screw_down for
// the four from below (the front Pi holes and the lid). *_use_insert = false
// gives self-tapping pilots instead.
insert_d      = 3.6;   insert_depth = 5;     // M2.5 x 4 heat-set insert, 1 mm past it for the melt (set insert_d to its maker's hole size)
screw_clear_d = 2.9;   screw_head_d = 5.0;   // M2.5 clearance; pocket for a socket or button head (4.5-4.7 mm)
screw_up      = 6;     // M2.5x6 from above (it may not reach the floor's bottom)
screw_down    = 8;     // M2.5x8 from below (M2.5x10 also fits)
// Pi: M2.5 into the standoffs (heat-set insert, or a self-tapping pilot). Each
// standoff is one taper, from standoff_d on the floor (less by the cooler's two
// screw heads under the board: standoff_base_d) to the Pi's 6 mm keep-out round
// the mounting hole, where the board rests.
standoff_d     = 7.5;
standoff_top_d = 6.0;
cooler_head_d  = 4.5;   // the Armor Lite's screw heads under the board (reference model)
cooler_head_h  = 2.0;
pi_use_insert = true;
// both blind: 5 mm leaves 1 mm of floor under them (standoff 4 + floor 2), and an
// M2.5x6 from above (4.6 mm below the PCB) clears the bottom
pi_pilot_d    = 2.2;   pi_pilot_depth = 5;     // M2.5 self-tapping
pi_insert_d   = insert_d;   pi_insert_depth = insert_depth;
// Front Pi holes: M2.5 from below through standoff + PCB into a lid boss.
// [front-left, front-right]; false = normal M2.5x6 from above, no lid boss there
lid_through_pi = [true, true];
thru_clear_d   = screw_clear_d;   thru_head_d = screw_head_d;
thru_tunnel    = 1.5;   // plastic left between the head and the PCB
// The front lid bosses are pillar_d wide like the lid-screw ones, less part_clear
// round every part of the board beside them (the USB-C receptacle, the cooler,
// the camera/display connectors, the PoE header and its pins), the same outline
// all the way up with its corners rounded by thru_round. They end thru_gap above
// the board, never on it: clear of its small parts, and of a board or a print a
// little thicker than drawn (the lid closes on the base, not on the board; the
// back screws hold the board, the front ones locate it).
thru_gap       = 1.5;
part_clear     = 0.3;
thru_round     = 1.5;
thru_pilot_depth = 9;     // self-tapping pilot; with inserts, room above the insert for a longer screw
thru_use_insert = true;   // M2.5 heat-set inserts, as the back standoffs (pi_insert_d, pi_insert_depth)
// Lid: M2.5 from below through base pillars into heat-set inserts in the lid bosses
pillar_d       = 10.0;  // base pillars and lid bosses: 3 mm of plastic round an insert
lid_screw_clear_d = screw_clear_d;
lid_head_d     = screw_head_d;
lid_tunnel     = 4.0;   // plastic left between the head and the joint: screw_down reaches the whole insert
lid_use_insert = true;
lid_pilot_d    = 2.2;  lid_pilot_depth = 9;    // M2.5 self-tapping (lid_use_insert = false)
lid_insert_d   = insert_d;  lid_insert_depth = insert_depth;
lid_overrun    = 9;     // with inserts: a screw_clear_d hole this far above the joint, for a longer screw
lip_t = 1.2;  lip_h = 3.0;                       // alignment lip under the lid

/* [Vents] */
vent_d     = 36;    // round honeycomb over the fan
vent_cell  = 4.0;   // hex hole across flats
vent_bar   = 1.3;   // plastic between hexes
vent2_c    = [60, 33];   // honeycomb patch over the heatsink's right fins / RP1
vent2_size = [18, 22];
side_slot_w = 2.0;  side_slot_pitch = 4.5;
floor_vents = true;
feet_recess = true;  foot_d = 10.5;  foot_depth = 0.8;   // stick-on rubber feet

/* [Lid text] */
text_on    = true;
text_str   = "Off The Cloud";
text_font  = "Liberation Sans:style=Bold";
text_size  = 7.0;
text_depth = 0.7;    // debossed into the top
text_pos   = [44, 55.3];
// the web address, a smaller line under the name (clear of the grilles)
url_on     = true;
url_str    = "off-the.cloud";
url_size   = 4.5;
url_pos    = [44, 47.8];

/* [Exploded / test] */
explode     = 28;
test_depth  = 4;     // how much of the case behind the USB end wall the test slice keeps

// ----------------------------------------------------------------------------
// Derived levels
// ----------------------------------------------------------------------------
$fn = 48;
eps = 0.01;

z_pb    = floor_t + standoff_h;         // PCB bottom
z_pt    = z_pb + pcb_t;                 // PCB top
z_ceil  = z_pt + inner_h;               // lid ceiling (inside)
z_top   = z_ceil + top_t;               // outside top
z_split = z_pt + max(usbc_zc + usbc_open[1]/2, hdmi_zc + hdmi_open[1]/2);  // base/lid joint

x_end_out   = pcb[0] + port_overhang - usb_proud;   // flat USB end, outer face
x_end_in    = x_end_out - wall;
y_front_out = -board_gap - wall;                    // flat USB-C/HDMI side, outer face
x_left_out  = -board_gap - wall;                    // microSD end wall (bay), outer face
y_back_out  = pcb[1] + back_gap + wall;

usb_open_top = z_pt + usb_h + port_clear;            // top of the blue/black stack cut-outs
led_hi_z = usb_open_top + led_bar + led_bore/2;     // both_above: LEDs above the blue stack (in the lid)
// between: the LED gap between the Ethernet jack and the blue stack
gap_y      = (eth_y + eth_w/2 + usb3_y - usb_w/2) / 2;
led_slot   = [led_rect[0] + 2*led_rect_clear, led_rect[1] + 2*led_rect_clear];
led_top_zc = z_pt + usb_port_zc[1];           // beside the top blue port, in the lid
led_bot_zc = z_split - led_slot[1]/2;         // beside the bottom blue port: a notch in the base the lid closes
gap_web    = (usb3_y - usb_w/2 - gap_clear) - (eth_y + eth_w/2 + gap_clear);
// gap: two 3 mm LEDs on the gap's line. The top one clears the cut-outs its bore
// overlaps (the Ethernet's and the blue stack's) by led_bar; the bottom one sits
// on the floor, under the cut-outs and the board.
port_cut_bottom = z_pt + port_z0 - port_clear;
eth_open_top    = z_pt + port_z0 + eth_h + port_clear;
function bore_overlaps(y, lo, hi) = y + led_bore/2 > lo && y - led_bore/2 < hi;
led_up_z = max(bore_overlaps(gap_y, eth_y - eth_w/2 - port_clear, eth_y + eth_w/2 + port_clear) ? eth_open_top : 0,
               bore_overlaps(gap_y, usb3_y - usb_w/2 - port_clear, usb3_y + usb_w/2 + port_clear) ? usb_open_top : 0)
           + led_bar + led_bore/2;
led_dn_z = floor_t + led_low_floor + led_seat_d/2;

// Standoffs: the widest a standoff may be at the height of the cooler's screw
// heads under the board (0.4 mm clear of them), and the foot that keeps every
// standoff's taper that narrow from 0.3 mm under the heads up
function head_room_d(h) = 2 * (min([for (c = cooler_holes) norm(h - c)]) - cooler_head_d/2 - 0.4);
standoff_base_d = min(standoff_d, standoff_top_d + (min([for (h = pi_holes) head_room_d(h)]) - standoff_top_d)
                                                   * standoff_h / (cooler_head_h + 0.3));
thru_head_depth = z_pb - thru_tunnel;   // head pocket from below, up into the standoff
lid_head_depth  = z_split - lid_tunnel; // head pocket from below, up into the pillar

// lid screw positions: inside lobes, merged with the wall, the head counterbore
// kept 0.8 mm inside the softened bottom edge
function lobe_pt(l, a) = let(d = l[2] - soft_inset(0) - lid_head_d/2 - 0.8)
                         [l[0] + d*cos(a), l[1] + d*sin(a)];
lid_screws = [ lobe_pt(lobes[1], 100),   // back-left (T1)
               lobe_pt(lobes[3],  70) ]; // back-right (T3)
thru_holes = [for (i = [0, 1]) if (lid_through_pi[i]) pi_holes[i]];   // front Pi holes: screws from below into the lid
function is_thru(h) = len([for (t = thru_holes) if (t == h) 1]) > 0;

echo(str("OTC case: z PCB bottom=", z_pb, " PCB top=", z_pt, " split=", z_split,
         " ceiling=", z_ceil, " top=", z_top));
echo(str("OTC case: LEDs ", led_layout, " at ", led_positions(),
         (led_layout == "between") ? str(", gap web ", gap_web, " mm, bars beside each LED ", (gap_web - led_slot[0])/2, " mm,",
                                         " lower LED bottom ", led_bot_zc - led_slot[1]/2 - z_pt, " mm above the PCB") :
         (led_layout == "gap") ? str(", top bore ", led_up_z - led_bore/2 - max(eth_open_top, usb_open_top), " mm over the port cut-outs, ",
                                     led_seat_d/2 + led_up_z, " seat top (ceiling ", z_ceil, "); bottom bore ",
                                     port_cut_bottom - led_dn_z - led_bore/2, " mm under the cut-outs, its leads ",
                                     z_pb - led_dn_z, " mm under the PCB") : ""));
echo(str("OTC case: lid screws=", lid_screws, " through the Pi=", thru_holes,
         " standoffs ", standoff_base_d, " to ", standoff_top_d, "; screws M2.5x", screw_up, " from above (tip ",
         z_pt - screw_up, " above the bottom, ", (z_pt - screw_up) - (z_pb - pi_insert_depth), " short of the hole's end), M2.5x",
         screw_down, " from below (", screw_down - thru_tunnel - pcb_t - thru_gap, " mm into the front inserts, ",
         screw_down - lid_tunnel, " into the lid's)"));

// ----------------------------------------------------------------------------
// 2D outlines
// ----------------------------------------------------------------------------
module outline_raw() {
  intersection() {
    union() {
      hull() {
        translate([x_left_out + box_r[0], y_front_out + box_r[0]]) circle(r = box_r[0]);
        translate([x_end_out  - box_r[1], y_front_out + box_r[1]]) circle(r = box_r[1]);
        translate([x_end_out  - box_r[2], y_back_out  - box_r[2]]) circle(r = box_r[2]);
        translate([x_left_out + box_r[3], y_back_out  - box_r[3]]) circle(r = box_r[3]);
      }
      for (l = lobes) translate([l[0], l[1]]) circle(r = l[2], $fn = 120);
    }
    // clip: flat front (y >= y_front_out) and flat USB end (x <= x_end_out)
    translate([-300, y_front_out]) square([300 + x_end_out, 400]);
  }
}

// the cloud (what you see from above; the lid follows it)
module outline_lid2d() {
  offset(r = corner_round) offset(delta = -corner_round)
    offset(r = -cusp_fillet) offset(delta = cusp_fillet)
      outline_raw();
}

// the base: the cloud minus the port bay on the microSD end
module bay2d() {
  translate([-200, bay_y[0]])
    offset(r = bay_r) offset(delta = -bay_r)
      square([200 + x_left_out, bay_y[1] - bay_y[0]]);
}
module outline_base2d() {
  offset(r = 1) offset(delta = -1) difference() { outline_lid2d(); bay2d(); }
}

// inside of the case (base and lid): the base outline less one wall
module cavity2d() { offset(delta = -wall) outline_base2d(); }

// printable rounded edge: 45 deg chamfer up to the tangent point, then a quarter round
function soft_inset(z) =
  let(R = soft_R, zt = R * (1 - sqrt(2)/2))
  (z <= zt) ? (2*zt - z) :
  (z >= R)  ? 0 :
  R - sqrt(R*R - (R - z)*(R - z));

// an outline extruded from z0 to z1, the edge at z0 (bottom) or z1 (top) softened
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
// Cutters shared by base and lid
// ----------------------------------------------------------------------------
// rounded rectangle through a vertical wall: centre (u, zc), size [w, h];
// axis "y" = wall facing -Y (front), "x" = wall facing X (ends); x0..x1 = depth range
module wall_hole(axis, u, zc, w, h, r, x0, x1) {
  if (axis == "y")
    translate([u, x1, zc]) rotate([90, 0, 0])
      linear_extrude(x1 - x0) offset(r = r) offset(delta = -r) square([w, h], center = true);
  else
    translate([x0, u, zc]) rotate([90, 0, 90])
      linear_extrude(x1 - x0) offset(r = r) offset(delta = -r) square([w, h], center = true);
}

// Front (USB-C / micro-HDMI): openings sized for the plugs. In the base they run
// up through the joint (U-slots, so the board drops straight in); the lid's
// wall closes them at z_split.
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
// (base part = U-slots, lid part = notches). Port faces end flush with the wall.
module end_ports_cut() {
  x0 = x_end_in - 0.6;  x1 = x_end_out + 1;
  zb = z_pt + port_z0 - port_clear;
  et = z_pt + port_z0 + eth_h + port_clear;
  gc = (led_layout == "between") ? gap_clear : port_clear;   // the sides facing the LED gap
  // [low y, high y] of each opening
  eth  = [eth_y - eth_w/2 - port_clear, eth_y + eth_w/2 + gc];
  usb3 = [usb3_y - usb_w/2 - gc, usb3_y + usb_w/2 + port_clear];
  usb2 = [usb2_y - usb_w/2 - port_clear, usb2_y + usb_w/2 + port_clear];
  wall_hole("x", (eth[0] + eth[1])/2, (zb + et)/2, eth[1] - eth[0], et - zb, 0.8, x0, x1);
  for (o = [usb3, usb2])
    wall_hole("x", (o[0] + o[1])/2, (zb + usb_open_top)/2, o[1] - o[0], usb_open_top - zb, 0.8, x0, x1);
}

// 3 mm LED: bore + flange counterbore from the inside face of the USB end wall
module led_hole(y, z) {
  translate([x_end_in - 1, y, z]) rotate([0, 90, 0]) {
    cylinder(d = led_bore, h = wall + 3, $fn = 40);
    cylinder(d = led_seat_d, h = 1 + led_seat_depth, $fn = 40);
  }
  translate([x_end_out - 0.4, y, z]) rotate([0, 90, 0])   // small outside chamfer
    cylinder(d1 = led_bore, d2 = led_bore + 1.2, h = 0.6 + eps, $fn = 40);
}

// 2 x 5 mm rectangular LED: a slot through the end wall, upright. The bottom
// one's slot runs up through the joint (a notch in the base the lid closes).
module led_slot_cut(y, zc, through_joint) {
  top = through_joint ? z_split + eps : zc + led_slot[1]/2;
  translate([x_end_in - 1, y - led_slot[0]/2, zc - led_slot[1]/2])
    cube([wall + 3, led_slot[0], top - (zc - led_slot[1]/2)]);
}

function led_positions() =
  (led_layout == "both_above") ? [[usb3_y - led_pair_pitch/2, led_hi_z], [usb3_y + led_pair_pitch/2, led_hi_z]]
  : (led_layout == "gap")      ? [[gap_y, led_up_z], [gap_y, led_dn_z]]
  :                              [[gap_y, led_top_zc], [gap_y, led_bot_zc]];

module leds_cut() {
  if (led_layout == "between") {
    led_slot_cut(gap_y, led_top_zc, false);
    led_slot_cut(gap_y, led_bot_zc, true);
  } else
    for (p = led_positions()) led_hole(p[0], p[1]);
}

// microSD end (back wall of the port bay): card slot + finger recess,
// power-button hole, on-board status-LED window
module left_end_cut() {
  x0 = x_left_out - 30;  x1 = x_left_out + wall + 0.4;
  wall_hole("x", sd_y, z_pt + sd_zc, sd_open[0], sd_open[1], 0.6, x0, x1);
  translate([x0, sd_y, z_pt + sd_zc]) rotate([0, 90, 0])
    linear_extrude(x_left_out + sd_recess[2] - x0)
      offset(r = 3) offset(delta = -3) square([sd_recess[1], sd_recess[0]], center = true);
  translate([x0, pwr_y, z_pt + pwr_zc]) rotate([0, 90, 0])
    cylinder(d = pwr_hole_d, h = x1 - x0, $fn = 32);
  translate([x_left_out - eps, pwr_y, z_pt + pwr_zc]) rotate([0, 90, 0])  // countersink: guides a pen tip
    cylinder(d1 = pwr_hole_d + 3, d2 = pwr_hole_d, h = 1.5, $fn = 32);
  wall_hole("x", act_y, z_pt + act_zc, act_window[0], act_window[1], 0.9, x0, x1);
}

// ----------------------------------------------------------------------------
// Base
// ----------------------------------------------------------------------------
module base_shell() { soft_body(0, z_split, false) outline_base2d(); }

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
      // lid-screw pillars, merged into the lobe walls, never through the skin
      intersection() {
        for (p = lid_screws) translate([p[0], p[1], 0]) cylinder(d = pillar_d, h = z_split);
        base_shell();
      }
    }
    // Pi screw pilots / inserts (back holes)
    for (h = pi_holes) if (!is_thru(h)) {
      if (pi_use_insert)
        translate([h[0], h[1], z_pb - pi_insert_depth]) cylinder(d = pi_insert_d, h = pi_insert_depth + 1);
      else
        translate([h[0], h[1], z_pb - pi_pilot_depth]) cylinder(d = pi_pilot_d, h = pi_pilot_depth + 1);
    }
    // front Pi holes: through hole + counterbore from below
    for (h = thru_holes) {
      translate([h[0], h[1], -1]) cylinder(d = thru_clear_d, h = z_pb + 2);
      translate([h[0], h[1], -1]) cylinder(d = thru_head_d, h = thru_head_depth + 1);
    }
    // lid screws: clearance + counterbore from below
    for (p = lid_screws) {
      translate([p[0], p[1], -1]) cylinder(d = lid_screw_clear_d, h = z_split + 2);
      translate([p[0], p[1], -1]) cylinder(d = lid_head_d, h = lid_head_depth + 1);
    }
    front_ports_cut();
    end_ports_cut();
    left_end_cut();
    leds_cut();
    if (floor_vents) floor_vents_cut();
    if (feet_recess) feet_cut();
  }
}

module floor_vents_cut() {
  // slots under the SoC/RAM area; clear of standoffs, cooler screws, LED wiring
  for (x = [14 : 4 : 54])
    translate([x, 0, -1]) linear_extrude(floor_t + 2)
      hull() { translate([0, 15]) circle(d = 2, $fn = 16); translate([0, 43]) circle(d = 2, $fn = 16); }
}

module feet_cut() {
  for (p = [[13, 5], [76, 4], [7, 44], [80, 52]])   // back-left kept off the insert hole above it
    translate([p[0], p[1], -1]) cylinder(d = foot_d, h = foot_depth + 1, $fn = 48);
}

// ----------------------------------------------------------------------------
// Lid
// ----------------------------------------------------------------------------
module lid_shell() { soft_body(z_split, z_top, true) outline_lid2d(); }

// alignment lip: hangs inside the base wall where there are no ports
module lip_region2d() {
  difference() {
    translate([-100, -100]) square([300, 300]);
    // front: lip only left of the USB-C and right of the MIPI connectors
    difference() {
      translate([-100, -100]) square([300, 100 + 4]);
      translate([-100, -100]) square([100 + 4, 200]);
      translate([58, -100]) square([26, 200]);
    }
    translate([78, -100]) square([100, 300]);                                     // USB end
    translate([-100, bay_y[0] - 1]) square([100 + 4, bay_y[1] - bay_y[0] + 2]);   // port bay wall
    for (p = lid_screws) translate(p) circle(d = pillar_d + 2 * fit_clear + 0.6);
  }
}

// a front lid boss's outline round the Pi's front hole h: a pillar_d circle
// less every part of the board beside it (part_clear larger), whatever its
// height, so the boss is one clean block from the ceiling down - against the
// front wall, its corners rounded (thru_round) and no thin fin left between
// two parts
module thru_boss2d(h) {
  offset(r = thru_round) offset(delta = -thru_round) difference() {
    translate(h) circle(d = pillar_d);
    projection() pi5_ref(0, false, false, 0, part_clear);
  }
}

// a front lid boss, down to thru_gap over the Pi's front hole h: that outline,
// merged into the lid's walls above the joint and inside the base's walls
// below it
module thru_boss(h) {
  intersection() {
    translate([0, 0, z_pt + thru_gap]) linear_extrude(z_ceil - z_pt) thru_boss2d(h);
    union() {
      translate([0, 0, z_pt]) linear_extrude(z_split - z_pt + eps) offset(delta = -fit_clear) cavity2d();
      lid_shell();
    }
  }
}

// its hole: an M2.5 insert's (with room above it for a longer screw), or a
// self-tapping pilot
module thru_boss_hole(h) {
  translate([h[0], h[1], z_pt + thru_gap - 1]) {
    if (thru_use_insert) {
      cylinder(d = pi_insert_d, h = pi_insert_depth + 1);
      cylinder(d = thru_clear_d, h = thru_pilot_depth + 1);
    } else
      cylinder(d = pi_pilot_d, h = thru_pilot_depth + 1);
  }
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
      // screw bosses hanging from the ceiling down to the joint
      intersection() {
        for (p = lid_screws) translate([p[0], p[1], z_split]) cylinder(d = pillar_d, h = z_ceil - z_split + 0.5);
        lid_shell();
      }
      // bosses down to the Pi's front holes
      for (h = thru_holes) thru_boss(h);
    }
    // boss pilots / inserts (blind, from the joint upwards)
    for (p = lid_screws)
      if (lid_use_insert)
        translate([p[0], p[1], z_split - 1]) {
          cylinder(d = lid_insert_d, h = lid_insert_depth + 1);
          cylinder(d = lid_screw_clear_d, h = lid_overrun + 1);
        }
      else
        translate([p[0], p[1], z_split - 1]) cylinder(d = lid_pilot_d, h = lid_pilot_depth + 1);
    for (h = thru_holes) thru_boss_hole(h);
    end_ports_cut();
    leds_cut();
    top_vents_cut();
    side_vents_cut();
    if (text_on)
      translate([text_pos[0], text_pos[1], z_top - text_depth])
        linear_extrude(text_depth + 1)
          text(text_str, size = text_size, font = text_font, halign = "center", valign = "center", $fn = 24);
    if (url_on)
      translate([url_pos[0], url_pos[1], z_top - text_depth])
        linear_extrude(text_depth + 1)
          text(url_str, size = url_size, font = text_font, halign = "center", valign = "center", $fn = 24);
  }
}

module honeycomb2d(w, h) {
  pitch = vent_cell + vent_bar;
  r = vent_cell / sqrt(3);          // circumradius of a hex with that across-flats
  for (i = [-ceil(w/pitch) - 1 : ceil(w/pitch) + 1], j = [-ceil(h/(pitch*0.866)) - 1 : ceil(h/(pitch*0.866)) + 1])
    translate([i * pitch + (j % 2 == 0 ? 0 : pitch/2), j * pitch * sqrt(3)/2])
      rotate(30) circle(r = r, $fn = 6);
}

module top_vents_cut() {
  translate([0, 0, z_ceil - 1]) linear_extrude(top_t + 2) {
    translate(fan_c) intersection() { circle(d = vent_d, $fn = 96); honeycomb2d(vent_d, vent_d); }
    translate(vent2_c) intersection() {
      offset(r = 3) offset(delta = -3) square(vent2_size, center = true);
      honeycomb2d(vent2_size[0], vent2_size[1]);
    }
  }
}

// vertical slots through the lid walls: front face, and radially round the top
// lobes (cut only where they meet wall material)
module side_vents_cut() {
  z0 = z_split + 3.5;  z1 = z_ceil - 2.5;
  module slot(len) hull() {
    translate([0, 0, 1]) rotate([0, 90, 0]) cylinder(d = side_slot_w, h = len, $fn = 16);
    translate([0, 0, z1 - z0 - 1]) rotate([0, 90, 0]) cylinder(d = side_slot_w, h = len, $fn = 16);
  }
  // front face, above the HDMI/USB-C openings (not where a front boss meets it)
  for (x = [6 : side_slot_pitch : 80]) if (min([for (h = thru_holes) abs(x - h[0])]) > pillar_d/2 + side_slot_w/2 + 0.5)
    translate([x, y_front_out - 1, z0]) rotate(90) slot(wall + 2);
  // top lobes: rays from inside the cloud
  intersection() {
    difference() {
      translate([0, 0, z0]) linear_extrude(z1 - z0) difference() { offset(delta = 0.5) outline_lid2d(); cavity2d(); }
      for (p = lid_screws) translate([p[0], p[1], 0]) cylinder(d = pillar_d + 5, h = 100);
      translate([x_end_in - 6, -50, 0]) cube([50, 200, 100]);       // USB end wall stays whole
      translate([-100, -50, 0]) cube([100 + 3, 200, 100]);         // solid lobe over the bay
    }
    c0 = [40, 30];
    for (a = [38 : 5.0 : 142])
      translate([c0[0], c0[1], z0]) rotate(a) slot(90);
  }
  // big left lobe (solid over the port bay): straight slots right through it and
  // the wall behind it, so the air the heatsink blows out of its left end can leave
  for (y = [9 : side_slot_pitch : 48])
    translate([-30, y, z0]) slot(30 + x_left_out + wall + 1);
}

// ----------------------------------------------------------------------------
// Test slice of the USB end wall (base + lid end fused), with a ledge the
// PCB's USB end can rest on while the ports are slid into the openings.
// ----------------------------------------------------------------------------
module test_usb_wall() {
  // the base and lid ends as one piece (same outline, cut-outs and holes as the
  // real parts; built as a single body so the STL stays a clean manifold)
  intersection() {
    translate([x_end_in - test_depth, -100, -1]) cube([test_depth + 20, 300, 100]);
    union() {
      difference() {
        intersection() {
          soft_body(0, z_top, false) outline_base2d();
          soft_body(0, z_top, true) outline_base2d();
        }
        translate([0, 0, floor_t]) linear_extrude(z_ceil - floor_t) cavity2d();
        end_ports_cut();
        leds_cut();
        if (feet_recess) feet_cut();
      }
      // PCB rest ledge (test piece only) + ribs tying it to the floor strip; a
      // channel through the ledge behind the bottom LED's hole lets it go in
      difference() {
        intersection() {
          translate([x_end_in - test_depth - 1, 1, z_pb - 1.6]) cube([test_depth + 1.5, pcb[1] - 2, 1.6]);
          translate([0, 0, -1]) linear_extrude(100) offset(delta = 0.5) cavity2d();
        }
        if (led_layout == "gap")
          translate([x_end_in - test_depth - 2, gap_y - led_seat_d/2 - 0.5, 0]) cube([test_depth + 2.5, led_seat_d + 1, 20]);
      }
      for (y = [6, 50])
        translate([x_end_in - test_depth - 1, y, floor_t - 0.5]) cube([test_depth + 1.5, 1.6, z_pb - floor_t + 0.5]);
    }
  }
}

// ----------------------------------------------------------------------------
// Test piece for the heat-set inserts: a lid back boss (pillar_d, its insert
// hole lid_insert_depth deep), a back Pi standoff (its taper, its insert hole)
// and the end of the front-left lid
// boss (the tightest: beside the USB-C receptacle and under the cooler, cut
// exactly as in the lid), standing on a small plate the way they are pressed:
// hole up.
// ----------------------------------------------------------------------------
module test_inserts() {
  boss_h = lid_insert_depth + 2;
  post_h = floor_t + standoff_h;
  front_h = pi_insert_depth + 3;   // of the front-left boss, from its end
  fl = pi_holes[0];
  if (is_thru(fl))
    translate([30 - fl[0], 6 - fl[1], front_h + 1]) mirror([0, 0, 1]) translate([0, 0, -(z_pt + thru_gap)])
      intersection() {
        difference() { thru_boss(fl); thru_boss_hole(fl); }
        translate([-50, -50, z_pt + thru_gap - eps]) cube([100, 100, front_h + 2*eps]);
      }
  difference() {
    union() {
      cube([36, 12, 1.2]);
      translate([6, 6, 0]) cylinder(d = pillar_d, h = boss_h);
      translate([18, 6, 0]) {
        cylinder(d = standoff_base_d, h = floor_t + eps);
        translate([0, 0, floor_t]) cylinder(d1 = standoff_base_d, d2 = standoff_top_d, h = standoff_h);
      }
    }
    translate([6, 6, boss_h - lid_insert_depth]) cylinder(d = lid_insert_d, h = lid_insert_depth + 1);
    translate([18, 6, post_h - pi_insert_depth]) cylinder(d = pi_insert_d, h = pi_insert_depth + 1);
  }
}

// ----------------------------------------------------------------------------
// Reference Raspberry Pi 5 + Armor Lite V5 + LEDs (renders only, never exported)
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
    // USB-C + micro HDMI (front edge)
    color("silver") blk([usbc_x - 4.45, -1.3, t + usbc_zc - 1.6], [8.9, 7.3, 3.2]);
    for (x = hdmi_x) color("silver") blk([x - 3.3, -1.5, t + hdmi_zc - 1.7], [6.6, 7.5, 3.4]);
    // Ethernet + USB stacks (end)
    e = pcb[0] + port_overhang;
    color([0.75, 0.75, 0.75]) blk([e - 21.2, eth_y - eth_w/2, t + port_z0], [21.2, eth_w, eth_h]);
    color([0.2, 0.2, 0.2]) translate([e - 0.2, eth_y - 6, t + port_z0 + 2]) cube([0.3, 12, 9]);
    for (st = [[usb3_y, [0.1, 0.3, 0.95]], [usb2_y, [0.1, 0.1, 0.1]]]) {
      color([0.8, 0.8, 0.8]) blk([e - 17.2, st[0] - usb_w/2, t + port_z0], [17.2, usb_w, usb_h - port_z0]);
      for (zc = usb_port_zc) color(st[1]) translate([e - 0.2, st[0] - 6.2, t + zc - 2.4]) cube([0.4, 12.4, 4.8]);
    }
    // GPIO header + pins
    color("black") blk([hdr_c[0] - 25.4, hdr_c[1] - 2.54, t], [50.8, 5.08, 2.5]);
    color("gold") for (i = [0:19], j = [0:1])
      blk([hdr_c[0] - 24.13 + i * 2.54 - 0.32, hdr_c[1] - 1.27 + j * 2.54 - 0.32, t], [0.64, 0.64, 8.5]);
    // fan connector, PoE header, MIPI connectors
    color("white") blk([65.4, 40.4, t], [4.3, 7.3, 5]);
    color("black") blk([59.0, 7.0, t], [5.1, 5.0, 2.5]);
    color("gold") for (i = [0:1], j = [0:1])   // its 2 x 2 pins, as tall as the GPIO's (a PoE HAT takes both)
      blk([59.0 + 1.28 + i * 2.54 - 0.32, 7.0 + 1.23 + j * 2.54 - 0.32, t], [0.64, 0.64, 8.5]);
    color("black") for (x = [47.3, 53.5]) blk([x, 0.5, t], [3.0, 16, 4.1]);
    // power button + on-board status LED
    color("white") blk([-0.45, pwr_y - 1.5, t + pwr_zc - 1.15], [3, 3, 2.3]);
    color("lime") blk([0, act_y - 0.8, t + act_zc - 0.5], [1.2, 1.6, 1]);
    // Armor Lite V5
    color([0.12, 0.12, 0.14]) {
      for (p = cooler_holes) cyl([p[0], p[1], t], 5, al_total_h - al_body_h + eps);
      translate([0, 0, t + al_total_h - al_body_h - sd - g]) linear_extrude(al_body_h + su + sd + 2*g) offset(delta = g) difference() {
        armor_footprint2d();
        translate(fan_c) circle(d = fan_d + 1);
      }
    }
    color([0.3, 0.3, 0.32]) cyl([fan_c[0], fan_c[1], t + al_total_h + fan_proud - 10], fan_d, 10);
    color([0.2, 0.2, 0.2]) translate([fan_c[0], fan_c[1], t + al_total_h + fan_proud]) cylinder(d = 12, h = 0.3);
    if (extras) {
      // microSD card (under the PCB, sticking out ~2 mm; inserted after assembly)
      color([0.25, 0.25, 0.3]) translate([-2.2, sd_y - 5.5, -1.6]) cube([14, 11, 0.8]);
      // Armor Lite screws under the board
      color("silver") for (p = cooler_holes) translate([p[0], p[1], -cooler_head_h]) cylinder(d = cooler_head_d, h = cooler_head_h);
    }
  }
  if (extras) {
    // Pi screws from above (the front ones come up from below into the lid)
    color("silver") for (h = pi_holes) if (!is_thru(h)) translate([h[0], h[1], z_pt]) cylinder(d = 4.5, h = 1.8);
    // status LEDs (3 mm, pushed in from inside; flange in the counterbore)
    for (p = led_positions()) led_ref(p[0], p[1]);
  }
}

module led_ref(y, z) {
  if (led_layout == "between") led_rect_ref(y, z); else led_round_ref(y, z);
}

// 2 x 5 mm rectangular LED, 7 mm long, its front 1.5 mm proud of the wall;
// leads back along the gap between the Ethernet jack and the blue stack
module led_rect_ref(y, z) {
  front = x_end_out + 1.5;
  color([1, 0.2, 0.15, 0.85]) translate([front - 7, y - led_rect[0]/2, z - led_rect[1]/2]) cube([7, led_rect[0], led_rect[1]]);
  color("silver") for (dz = [-2.54, 0, 2.54]) translate([front - 7 - 8, y - 0.2, z + dz - 0.2]) cube([8, 0.4, 0.4]);
}

module led_round_ref(y, z) {
  fx = x_end_in + led_seat_depth;   // flange's front face
  translate([0, y, z]) {
    color([1, 0.2, 0.15, 0.85]) translate([fx - 1, 0, 0]) rotate([0, 90, 0]) cylinder(d = 3.8, h = 1, $fn = 32);
    color([1, 0.2, 0.15, 0.85]) translate([fx, 0, 0]) rotate([0, 90, 0]) cylinder(d = 3.0, h = 3.8, $fn = 32);
    color([1, 0.2, 0.15, 0.85]) translate([fx + 3.8, 0, 0]) sphere(d = 3.0, $fn = 32);
    color("silver") for (dy = [-1.27, 0, 1.27]) translate([fx - 9, dy - 0.2, -0.2]) cube([8, 0.4, 0.4]);
  }
}

// ----------------------------------------------------------------------------
// Part selection
// ----------------------------------------------------------------------------
module base_col() { color([0.93, 0.94, 0.96]) base(); }
module lid_col(a = 1) { color([0.80, 0.86, 0.96, a]) lid(); }
cut_col = [0.95, 0.55, 0.2];   // section faces in cut views (case)
pi_cut_col = [0.35, 0.4, 0.45];  // section faces in cut views (Pi)

if (part == "base" || part == "base_print") base();
else if (part == "lid") lid();
else if (part == "lid_print") translate([0, 0, z_top]) rotate([180, 0, 0]) lid();
else if (part == "test_usb_wall") test_usb_wall();
else if (part == "test_usb_wall_print")   // lying on the outer face of the end wall
  translate([0, 0, x_end_out]) rotate([0, 90, 0]) test_usb_wall();
else if (part == "test_inserts") test_inserts();
else if (part == "pi_ref") pi5_ref();
// interference checks: each must render as an empty object
else if (part == "check_static") intersection() { union() { base(); lid(); } pi5_ref(); }
else if (part == "check_base_drop") intersection() { base(); pi5_ref(40, true, false); }
else if (part == "check_parts") intersection() { base(); lid(); }
else if (part == "check_lid_drop") intersection() { lid(); pi5_ref(0, false, false, 40); }
else if (part == "outline") {   // quick look at the cloud: lid outline, base outline, board
  color([0.8, 0.86, 0.96]) linear_extrude(1) outline_lid2d();
  color([0.6, 0.6, 0.6]) translate([0, 0, 1]) linear_extrude(1) difference() { outline_base2d(); cavity2d(); }
  color([0.05, 0.45, 0.2]) translate([0, 0, 2]) linear_extrude(1) rrect2d(pcb, pcb_r);
  color("red") for (p = concat(lid_screws, thru_holes)) translate([p[0], p[1], 3]) cylinder(d = 3, h = 1);
}
else if (part == "assembly") {
  base_col(); lid_col(lid_alpha);
  if (show_pi) pi5_ref();
}
else if (part == "exploded") {
  base_col();
  if (show_pi) translate([0, 0, explode]) pi5_ref();
  translate([0, 0, 2.2 * explode]) lid_col(lid_alpha);
}
else if (part == "cut_usb") {
  // section through the blue USB stack and the LEDs (plane y = usb3_y), seen from -Y
  intersection() { union() { base_col(); lid_col(); } color(cut_col) translate([-50, usb3_y, -10]) cube([200, 200, 100]); }
  if (show_pi) intersection() { pi5_ref(); color(pi_cut_col) translate([-50, usb3_y, -10]) cube([200, 200, 100]); }
}
else if (part == "cut_front") {
  // section through the two front standoffs (plane y = 3.5), seen from +Y (inside)
  intersection() { union() { base_col(); lid_col(); } color(cut_col) translate([-50, -50, -10]) cube([200, 50 + 3.5, 100]); }
  if (show_pi) intersection() { pi5_ref(); color(pi_cut_col) translate([-50, -50, -10]) cube([200, 50 + 3.5, 100]); }
}
else if (part == "cut_side") {
  // section across the case through the fan and USB-C side (plane x = fan_c.x), seen from -X
  intersection() { union() { base_col(); lid_col(); } color(cut_col) translate([fan_c[0], -50, -10]) cube([200, 200, 100]); }
  if (show_pi) intersection() { pi5_ref(); color(pi_cut_col) translate([fan_c[0], -50, -10]) cube([200, 200, 100]); }
}
