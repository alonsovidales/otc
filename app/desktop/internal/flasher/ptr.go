// SPDX-License-Identifier: AGPL-3.0-or-later

package flasher

import "unsafe"

func uintptrOf(b []byte) uintptr { return uintptr(unsafe.Pointer(unsafe.SliceData(b))) }
