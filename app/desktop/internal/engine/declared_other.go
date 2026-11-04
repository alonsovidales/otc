// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package engine

// declaredMountPoints: none read on this system (declared.go).
func declaredMountPoints() []string { return nil }
