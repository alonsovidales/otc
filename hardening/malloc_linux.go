// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && cgo

package hardening

/*
#include <malloc.h>

// What ReturnFreedMemory asks of glibc's allocator: every block of
// threshold bytes or more in a mapping of its own, unmapped when it is
// freed, and at most arenas arenas.
static int otc_return_freed_memory(int threshold, int arenas) {
	return mallopt(M_MMAP_THRESHOLD, threshold) == 1 && mallopt(M_ARENA_MAX, arenas) == 1;
}
*/
import "C"

import "github.com/alonsovidales/otc/log"

// cFreedThreshold and cMallocArenas are ReturnFreedMemory's settings.
const (
	cFreedThreshold = 1 << 20
	cMallocArenas   = 2
)

// ReturnFreedMemory makes what the native libraries free (ONNX Runtime's
// tensors, OpenCV's images, the HEIC decoder's frames) go back to the
// system, for a device short of memory. glibc keeps freed memory to reuse
// it, and gives it back by MADV_DONTNEED - which Linux refuses for locked
// pages (LockMemory): a locked process's freed heap stayed resident for
// good. It also raises its threshold for a block of its own up to 32 MB
// each time such a block is freed, so after a few large frees nearly
// everything came from that heap. A fixed threshold keeps blocks of 1 MiB
// or more in mappings of their own, unmapped - returned, locked or not -
// when freed; two arenas keep the smaller ones from spreading over one
// heap per thread.
func ReturnFreedMemory() {
	if C.otc_return_freed_memory(C.int(cFreedThreshold), C.int(cMallocArenas)) == 0 {
		log.Error("could not set the allocator to return freed memory")
	}
}
