// SPDX-License-Identifier: AGPL-3.0-or-later

package facerecognition

import (
	"os"
	"path/filepath"
	"testing"
)

// A model file OpenCV can't read leaves face recognition off with an
// error; gocv alone aborted the whole process (and the test binary).
func TestNewRecognizerRefusesAModelOpenCVCantRead(t *testing.T) {
	dir := t.TempDir()
	det, rec := filepath.Join(dir, "det.onnx"), filepath.Join(dir, "rec.onnx")
	for _, p := range []string{det, rec} {
		if err := os.WriteFile(p, []byte("this is not an ONNX model"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := NewRecognizer(det, rec); err == nil {
		r.Close()
		t.Fatal("a garbage model was loaded")
	}
}
