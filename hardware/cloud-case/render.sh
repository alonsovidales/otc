#!/bin/sh
# SPDX-License-Identifier: CC-BY-SA-4.0
# Regenerates the STLs and preview PNGs from otc-case.scad.
# Needs OpenSCAD 2025+ (Manifold backend); on macOS: brew install --cask openscad@snapshot
set -e
cd "$(dirname "$0")"
OPENSCAD=${OPENSCAD:-/Applications/OpenSCAD.app/Contents/MacOS/OpenSCAD}
command -v "$OPENSCAD" >/dev/null 2>&1 || OPENSCAD=openscad

stl() {  # part file
  "$OPENSCAD" -D "part=\"$1\"" --export-format binstl -o "$2" otc-case.scad 2>&1 \
    | grep -E "WARNING|ERROR|Status|Volumes|Genus" | sed "s|^|$2: |"
}
png() {  # part file rx rz projection [extra -D]
  "$OPENSCAD" -D "part=\"$1\"" $6 --render --imgsize 1400,1050 \
    --camera=0,0,0,$3,0,$4,0 --viewall --autocenter --projection=$5 \
    --colorscheme=Tomorrow -o "$2" otc-case.scad 2>&1 | grep -E "WARNING|ERROR" || true
}

stl base_print          otc-case-base.stl
stl lid_print           otc-case-lid.stl
stl test_usb_wall_print otc-case-test-usb-wall.stl
stl test_inserts        otc-case-test-inserts.stl

png assembly    view-iso.png          58  32 p
png assembly    view-usb-end.png      82  90 o
png assembly    view-usb-end-empty.png 72 62 p "-D show_pi=false"
png assembly    view-port-side.png    82   0 o
png assembly    view-microsd-bay.png  72 -58 p
png lid         view-lid-top.png       0   0 o
png exploded    view-exploded.png     62  32 p
png cut_usb     view-cut-usb.png      90   0 o
png cut_front   view-cut-front.png    90 180 o
png cut_side    view-cut-side.png     90 270 o
png base        view-base-inside.png  50  25 p
png lid_print   view-lid-print.png    50  30 p
png base        view-bottom.png      180   0 o
png test_usb_wall_print view-test-usb-wall.png 45 30 p
png test_inserts        view-test-inserts.png  45 30 p

# interference checks (reference Pi vs case, board dropped in, lid lowered on,
# base vs lid): each result must be empty or zero-thickness contacts only
T=${TMPDIR:-/tmp}
for c in check_static check_base_drop check_lid_drop check_parts; do
  "$OPENSCAD" -D "part=\"$c\"" -o "$T/otc-$c.stl" otc-case.scad 2>&1 | grep -E "empty" | sed "s|^|$c: |" || true
done
