// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"math"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// units turns GB into the status's units (1.024 MB).
func units(gb float64) int64 { return int64(math.Round(gb * 1000 / 1.024)) }

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

func TestUsedOfTotal(t *testing.T) {
	for _, c := range []struct {
		used, size float64 // GB
		want       string
	}{
		{0, 0, "0.0 of 0.0 GB"},
		{0, 474, "0.0 of 474 GB"},
		{1.8, 8.5, "1.8 of 8.5 GB"},   // under 100 GB: one decimal
		{39.6, 64, "39.6 of 64.0 GB"}, // the Memory row's format
		{39.6, 474, "39.6 of 474 GB"}, // from 100 GB: whole
		{120.4, 474, "120 of 474 GB"},
		{1200, 3600, "1.2 of 3.6 TB"},     // from 1000 GB: TB
		{39.6, 3600, "39.6 GB of 3.6 TB"}, // units differ: both written
		{474, 1000, "474 GB of 1.0 TB"},
		{99.94, 99.96, "99.9 of 100 GB"}, // rounding picks the tier
		{999.4, 999.6, "999 GB of 1.0 TB"},
		{4000, 4000, "4.0 of 4.0 TB"},
		{12345, 16000, "12.3 of 16.0 TB"},
	} {
		if got := usedOfTotal(units(c.used), units(c.size)); got != c.want {
			t.Errorf("usedOfTotal(%v GB, %v GB) = %q, want %q", c.used, c.size, got, c.want)
		}
	}
}

func TestStorageUseTitle(t *testing.T) {
	st := config.State{RaidSummary: "Storage healthy", CPUPercent: 12, MemUsed: units(1.8), MemSize: units(8.5)}
	// No size reported yet: no Storage item, and the tooltip without it.
	if got := storageUseTitle(st); got != "" {
		t.Fatalf("size 0: %q", got)
	}
	if got, want := loadTip(st), "CPU: 12% · Memory: 1.8 of 8.5 GB"; got != want {
		t.Fatalf("tip without storage: got %q, want %q", got, want)
	}
	st.StorageUsed, st.StorageSize = units(39.6), units(474)
	if got, want := storageUseTitle(st), "Storage: 39.6 of 474 GB"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := loadTip(st), "Storage: 39.6 of 474 GB · CPU: 12% · Memory: 1.8 of 8.5 GB"; got != want {
		t.Fatalf("tip: got %q, want %q", got, want)
	}
	st.StorageUsed, st.StorageSize = units(1200), units(3600)
	if got, want := storageUseTitle(st), "Storage: 1.2 of 3.6 TB"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
