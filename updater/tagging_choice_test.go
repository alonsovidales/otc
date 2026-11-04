// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sqlLines is every statement line of the SQL heredocs in text, trimmed.
func sqlLines(text string) []string {
	var lines []string
	for _, m := range regexp.MustCompile(`(?s)<<'SQL'\n(.*?)\nSQL\n`).FindAllStringSubmatch(text, -1) {
		for _, line := range strings.Split(m[1], "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "--") {
				lines = append(lines, line)
			}
		}
	}
	return lines
}

// Release 158's choice of Image Tagging, made restartable: install.sh's
// schema block (new devices, re-runs, the container) and the corrected
// release script (the devices installed before) ask the same question -
// is the column still NULL-able, the mark of a run to finish - and run the
// same three statements: add it with no value, give it one from the switch,
// make it NOT NULL.
func TestTaggingChoiceIsTheSameInInstallAndCorrected158(t *testing.T) {
	install := readFile(t, "../scripts/install.sh")
	block := regexp.MustCompile(`(?s)# Release 158 \(updates/corrected/158\.sh\).*?\n    fi\n`).FindString(install)
	if block == "" {
		t.Fatal("install.sh's schema migrations have no release 158 block")
	}
	corrected := readFile(t, "../scripts/updates/corrected/158.sh")
	question := `"$(mysql "$db" -N -B -e "SELECT IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'settings' AND COLUMN_NAME = 'image_tagging_chosen'")" != NO`
	for name, text := range map[string]string{"install.sh": block, "corrected/158.sh": corrected} {
		if !strings.Contains(text, question) {
			t.Errorf("%s doesn't ask whether the column is NOT NULL yet", name)
		}
	}
	want := []string{
		"ALTER TABLE settings ADD COLUMN IF NOT EXISTS image_tagging_chosen TINYINT(1) NULL;",
		"UPDATE settings SET image_tagging_chosen = (image_tagging_enabled = 0) WHERE image_tagging_chosen IS NULL;",
		"ALTER TABLE settings MODIFY image_tagging_chosen TINYINT(1) NOT NULL DEFAULT 0;",
	}
	if got := sqlLines(block); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("install.sh's release 158 statements are\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := sqlLines(corrected); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("corrected/158.sh's statements are\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
