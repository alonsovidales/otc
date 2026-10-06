// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Issue #187: File.size is an int32, so from 2 GiB on it holds the size
// wrapped (a 3 GiB video reads negative). Devices from release 93 also send
// size64, the real size; older ones leave it at 0, and their size is read
// as before. Same rule as dao.FileSize on the device.
import type { File } from "../proto/messages";

/** f's size in bytes: size64, or size from a device before release 93. */
export const fileSize = (f: Pick<File, "size" | "size64">): number =>
  f.size64 ? Number(f.size64) : f.size;
