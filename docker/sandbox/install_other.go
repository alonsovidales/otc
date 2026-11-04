// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package main

import "errors"

var errNotLinux = errors.New("system-call filters are Linux's")

func install([]insn) error { return errNotLinux }

func refused() error { return errNotLinux }
