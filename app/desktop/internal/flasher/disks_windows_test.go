// SPDX-License-Identifier: AGPL-3.0-or-later

package flasher

import (
	"slices"
	"testing"
)

func TestWithEnv(t *testing.T) {
	env := []string{"=C:=C:\\x", "Path=C:\\Windows", "PSMODULEPATH=C:\\Users\\a\\Documents\\WindowsPowerShell\\Modules", "TEMP=C:\\t"}
	got := withEnv(env, "PSModulePath", `C:\Windows\system32\WindowsPowerShell\v1.0\Modules`)
	want := []string{"=C:=C:\\x", "Path=C:\\Windows", "TEMP=C:\\t", `PSModulePath=C:\Windows\system32\WindowsPowerShell\v1.0\Modules`}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestUnderADriveOrFolder(t *testing.T) {
	for _, tc := range []struct {
		p, top string
		want   bool
	}{
		{`E:\Photos`, `E:\`, true},
		{`e:\photos\2024`, `E:\`, true},
		{`F:\Photos`, `E:\`, false},
		{`C:\Mounts\Card\x`, `C:\Mounts\Card\`, true},
		{`C:\Mounts\Cards\x`, `C:\Mounts\Card\`, false},
	} {
		if got := under(tc.p, tc.top); got != tc.want {
			t.Errorf("under(%q, %q) = %v", tc.p, tc.top, got)
		}
	}
}
