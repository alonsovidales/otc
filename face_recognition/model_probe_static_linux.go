// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !customenv && opencvstatic

package facerecognition

// model_probe_linux.cpp's OpenCV headers for gocv's static build (its
// cgo_static.go); the libraries come with gocv's.

/*
#cgo CPPFLAGS: -I/usr/local/include -I/usr/local/include/opencv4
*/
import "C"
