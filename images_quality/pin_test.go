// SPDX-License-Identifier: AGPL-3.0-or-later

package imagesquality

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The model's download is pinned in three places - install.sh (fresh
// installs), update.sh (devices updating, while it is missing) and the
// container's entrypoint - which must agree on its address and hash, and on
// the file the config's [quality] section names.
func TestQualityModelPinIsTheSameEverywhere(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join("..", p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	url := regexp.MustCompile(`https://github\.com/alonsovidales/otc/releases/download/models/nima_mobilenet_aesthetic\.onnx`)
	sha := regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	var want string
	for _, f := range []string{"scripts/install.sh", "scripts/update.sh", "docker/entrypoint.sh"} {
		text := read(f)
		if !url.MatchString(text) {
			t.Errorf("%s doesn't fetch the model from its release", f)
		}
		line := regexp.MustCompile(`(?m)^.*(QUALITY_ONNX_SHA=|QUALITY_MODEL=).*$`).FindAllString(text, -1)
		var got string
		for _, l := range line {
			if h := sha.FindString(l); h != "" {
				got = h
			}
		}
		if got == "" {
			t.Errorf("%s has no pinned hash for the model", f)
			continue
		}
		if want == "" {
			want = got
		} else if got != want {
			t.Errorf("%s pins %s, another file %s", f, got, want)
		}
	}
	for _, f := range []string{"scripts/install.sh", "scripts/updates/150.sh", "docker/entrypoint.sh"} {
		if !regexp.MustCompile(`nima_mobilenet_aesthetic\.onnx`).MatchString(read(f)) {
			t.Errorf("%s doesn't name the model's file", f)
		}
	}
}
