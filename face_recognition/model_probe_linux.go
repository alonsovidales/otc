// SPDX-License-Identifier: AGPL-3.0-or-later

package facerecognition

/*
#cgo CXXFLAGS: --std=c++11
#include <stdlib.h>
#include "model_probe.h"
*/
import "C"

import (
	"errors"
	"strings"
	"unsafe"
)

// probeModels loads both models once where OpenCV's exceptions are caught.
// For a model file it can't read (truncated by a failing SD card, not
// ONNX at all) cv::dnn::readNet throws, and gocv's constructors don't catch
// it: it crossed the cgo boundary and aborted otc, at every start - not
// the "face recognition is not available" the feature is meant to degrade
// to. A few ms once at startup for models that do load. Linux only - the
// devices: linking OpenCV a second time makes macOS's linker warn on every
// build (model_probe_other.go).
func probeModels(detector, recognizer string) error {
	det := C.CString(detector)
	defer C.free(unsafe.Pointer(det))
	rec := C.CString(recognizer)
	defer C.free(unsafe.Pointer(rec))
	buf := make([]byte, 512)
	switch C.otc_probe_face_models(det, rec, (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))) {
	case 0:
		return nil
	case 2:
		return errors.New("a face model loaded empty")
	}
	msg, _, _ := strings.Cut(string(buf), "\x00")
	return errors.New(strings.TrimSpace(msg))
}
