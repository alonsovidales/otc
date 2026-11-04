#!/bin/sh
# SPDX-License-Identifier: CC-BY-SA-4.0
# Regenerates the STLs and preview PNGs from otc-screen-case.scad.
# Needs OpenSCAD 2025+ (Manifold backend); on macOS: brew install --cask openscad@snapshot
set -e
cd "$(dirname "$0")"
OPENSCAD=${OPENSCAD:-/Applications/OpenSCAD.app/Contents/MacOS/OpenSCAD}
command -v "$OPENSCAD" >/dev/null 2>&1 || OPENSCAD=openscad
F=otc-screen-case.scad

stl() {  # part file
  "$OPENSCAD" -D "part=\"$1\"" --export-format binstl -o "$2" $F 2>&1 \
    | grep -E "WARNING|ERROR|Status|Volumes|Genus" | sed "s|^|$2: |"
}
png() {  # part file rx rz projection [extra -D]
  "$OPENSCAD" -D "part=\"$1\"" $6 --render --imgsize 1400,1050 \
    --camera=0,0,0,$3,0,$4,0 --viewall --autocenter --projection=$5 \
    --colorscheme=Tomorrow -o "$2" $F 2>&1 | grep -E "WARNING|ERROR" || true
}

stl base_print   otc-screen-case-base.stl
stl lid_print    otc-screen-case-lid.stl
stl spacers      otc-screen-case-spacers.stl
stl test_keyhole otc-screen-case-test-keyhole.stl

png assembly     view-iso.png          58  32 p
png on_screen    view-on-screen.png   180 180 o
png base         view-wall-side.png   180 180 o
png base         view-base-inside.png  50 115 p
png lid_print    view-lid-print.png    50  30 p
png exploded     view-exploded.png     62  32 p
png cut_front    view-cut-front.png    90 180 o
png cut_keyhole  view-cut-keyhole.png  90  90 o
png test_keyhole view-test-keyhole.png 45  30 p
png cut_usb      view-cut-usb.png      90   0 o

# interference checks (reference Pi vs case, board dropped in, lid lowered on,
# base vs lid, the screen's screw heads): each result must be empty or
# zero-thickness contacts only
T=${TMPDIR:-/tmp}
for c in check_static check_base_drop check_lid_drop check_parts check_heads; do
  "$OPENSCAD" -D "part=\"$c\"" -o "$T/otc-screen-$c.stl" $F 2>&1 | grep -E "empty" | sed "s|^|$c: |" || true
done
