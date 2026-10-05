// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package facerecognition

// probeModels checks nothing off Linux: the devices run Linux, and a
// second link of OpenCV for the probe makes macOS's linker warn about
// duplicate libraries on every build (see model_probe_linux.go).
func probeModels(detector, recognizer string) error { return nil }
