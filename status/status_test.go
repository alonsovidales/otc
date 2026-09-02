package status

import "testing"

func TestDiskUsageRoot(t *testing.T) {
	all, used, free, err := diskUsage("/")
	if err != nil {
		t.Fatalf("diskUsage(\"/\") returned an error: %v", err)
	}

	if all == 0 {
		t.Error("expected a nonzero total disk size for \"/\"")
	}
	if used+free != all {
		t.Errorf("expected used (%d) + free (%d) to equal all (%d)", used, free, all)
	}
	if used > all {
		t.Errorf("used (%d) should never exceed all (%d)", used, all)
	}
}

func TestDiskUsageMissingPath(t *testing.T) {
	if _, _, _, err := diskUsage("/this/path/does/not/exist/hopefully"); err == nil {
		t.Error("expected an error for a nonexistent path")
	}
}
