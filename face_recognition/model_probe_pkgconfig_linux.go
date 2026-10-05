// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !customenv && !opencvstatic

package facerecognition

// model_probe_linux.cpp's OpenCV, found as gocv's default build finds it
// (its cgo.go).

/*
#cgo pkg-config: opencv4
*/
import "C"
