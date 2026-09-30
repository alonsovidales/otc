// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package main

// The device runs on Linux; elsewhere (a development Mac) there is nothing
// to lock or redirect.
func lockMemory()    {}
func secureTempDir() {}
