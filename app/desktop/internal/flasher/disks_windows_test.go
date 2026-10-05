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
