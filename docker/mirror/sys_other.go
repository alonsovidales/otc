// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package main

import "errors"

// otc-mirror runs in a Linux container; elsewhere only its tests build it.

func readEnds(string) error { return errors.New("not on Linux") }

func syncFS(string) {}
