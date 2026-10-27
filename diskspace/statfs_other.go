// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux && !darwin

package diskspace

func of(string) (Usage, error) { return Usage{}, errUnsupported }
