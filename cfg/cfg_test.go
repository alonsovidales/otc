package cfg

import (
	"testing"
)

func TestDebugLevel(t *testing.T) {
	if err := Init("config", "dev"); err != nil {
		t.Error("Test config file can't be loaded")
		t.Fail()
	}

	if GetStr("section1", "val_str") != "test" {
		t.Error("Expected value for section \"section1\" and \"val_str\" filed was \"test\"")
	}

	if GetInt("section1", "val_int") != 123 {
		t.Error("Expected value for section \"section1\" and \"val_int\" filed was \"123\"")
	}

	if !GetBool("section1", "val_bool_true") {
		t.Error("Expected value for section \"section1\" and \"val_bool_true\" filed was \"true\"")
	}

	if GetBool("section1", "val_bool_false") {
		t.Error("Expected value for section \"section1\" and \"val_bool_false\" filed was \"false\"")
	}

	if !GetBool("section2", "val_bool_true") {
		t.Error("Expected value for section \"section2\" and \"val_bool_true\" filed was \"true\"")
	}

	if GetBool("section2", "val_bool_false") {
		t.Error("Expected value for section \"section2\" and \"val_bool_false\" filed was \"false\"")
	}
}

func TestGetFloat(t *testing.T) {
	if err := Init("config", "dev"); err != nil {
		t.Fatal("Test config file can't be loaded")
	}

	if got := GetFloat("section1", "val_float"); got != 3.14 {
		t.Errorf("Expected value for section \"section1\" and \"val_float\" field was 3.14, got %v", got)
	}
}

func TestGetUint64(t *testing.T) {
	if err := Init("config", "dev"); err != nil {
		t.Fatal("Test config file can't be loaded")
	}

	if got := GetUint64("section1", "val_uint"); got != 456 {
		t.Errorf("Expected value for section \"section1\" and \"val_uint\" field was 456, got %v", got)
	}
}
