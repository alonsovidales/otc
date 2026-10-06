// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.File as PbFile

// Issue #187: File.size is int32, so from 2 GiB on it carries the size
// wrapped. Devices from release 93 also send size64, the real size; older
// ones leave it 0.

/** The file's size in bytes: size64, or size from a device before release 93. */
val PbFile.byteSize: Long get() = if (size64 != 0L) size64 else size.toLong()

/**
 * Whether a local file of [local] bytes is this file's size. Without size64
 * the device's size is wrapped, so the local one is too (toInt() wraps the
 * way the device's int32 does).
 */
fun PbFile.sizeMatches(local: Long): Boolean = if (size64 != 0L) local == size64 else local.toInt() == size
