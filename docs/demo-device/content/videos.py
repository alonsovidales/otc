#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# A short phone-style clip from a few stills of one event: a gentle Ken Burns move over each
# (zoom in, zoom out or a slow pan, alternating), crossfades, a silent audio track, no text.
#   python3 videos.py out.mp4 2025-08-11T12:05:42.000000Z "+37.1110-008.6880/" still.jpg...
# 1920x1080, 30 fps, H.264 High + AAC, with creation_time and the ISO 6709 location a phone
# writes (read back by ffprobe as format tags "location" / "location-eng").
import subprocess, sys

out, created, location, stills = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4:]
SEG, FADE, FPS = 4.2, 0.8, 30
frames = int(SEG * FPS)
moves = [
    # zoom in on the centre, a little above it (faces sit high in most frames)
    ("min(1+0.0011*on,1.14)", "iw/2-(iw/zoom/2)", "ih*0.45-(ih/zoom/2)"),
    # slow pan left to right at a fixed zoom
    ("1.12", f"(iw-iw/zoom)*on/{frames}", "ih/2-(ih/zoom/2)"),
    # zoom out from slightly in
    ("max(1.14-0.0011*on,1)", "iw/2-(iw/zoom/2)", "ih*0.45-(ih/zoom/2)"),
    # pan right to left
    ("1.12", f"(iw-iw/zoom)*(1-on/{frames})", "ih/2-(ih/zoom/2)"),
]
args = ["ffmpeg", "-v", "error", "-y"]
for s in stills:
    args += ["-i", s]
total = len(stills) * SEG - (len(stills) - 1) * FADE
args += ["-f", "lavfi", "-t", f"{total:.2f}", "-i", "anullsrc=r=48000:cl=stereo"]
fl = []
for i in range(len(stills)):
    z, x, y = moves[i % len(moves)]
    # 16:9 crop (kept a little above centre), enlarged so the move doesn't step from pixel to pixel
    fl.append(f"[{i}:v]crop=iw:'min(ih,iw*9/16)':0:'(ih-min(ih,iw*9/16))*0.4',scale=3840:2160:flags=lanczos,"
              f"zoompan=z='{z}':x='{x}':y='{y}':d={frames}:s=1920x1080:fps={FPS},setsar=1,format=yuv420p[v{i}]")
last = "v0"
for i in range(1, len(stills)):
    fl.append(f"[{last}][v{i}]xfade=transition=fade:duration={FADE}:offset={i * (SEG - FADE):.2f}[x{i}]")
    last = f"x{i}"
args += ["-filter_complex", ";".join(fl), "-map", f"[{last}]", "-map", f"{len(stills)}:a",
         "-c:v", "libx264", "-preset", "slow", "-crf", "20", "-profile:v", "high", "-pix_fmt", "yuv420p", "-r", str(FPS),
         "-c:a", "aac", "-b:a", "96k", "-shortest", "-movflags", "+faststart",
         "-fflags", "+bitexact", "-flags:v", "+bitexact", "-flags:a", "+bitexact", "-map_metadata", "-1",
         "-metadata", f"creation_time={created}", "-metadata", f"location={location}", "-metadata", f"location-eng={location}",
         "-metadata:s:v", f"creation_time={created}", "-metadata:s:a", f"creation_time={created}", out]
import os
os.makedirs(os.path.dirname(out), exist_ok=True)
subprocess.run(args, check=True)
