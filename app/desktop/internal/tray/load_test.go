// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

func TestStorageTitle(t *testing.T) {
	st := config.State{RaidSummary: "Storage healthy"}
	if got := storageTitle(st); got != "Storage healthy" {
		t.Fatalf("no figures yet: %q", got)
	}
	st.StorageUsed, st.StorageSize = 420, 1000
	if got, want := storageTitle(st), "Storage healthy  ■■■■□□□□□□ 42% used"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	st.MemUsed, st.MemSize = 2000, 8000
	if got, want := memoryTitle(st), "Memory: 2.0 of 8.2 GB"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
