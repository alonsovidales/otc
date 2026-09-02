package profile

import (
	"bytes"
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

func TestInitFromPb(t *testing.T) {
	src := &pb.Profile{
		Name:   "Alice",
		Image:  []byte{1, 2, 3},
		Text:   "hello",
		Domain: "alice.off-the.cloud",
	}

	pr := InitFromPb(nil, src)

	if pr.Name != src.Name {
		t.Errorf("Name = %q, want %q", pr.Name, src.Name)
	}
	if !bytes.Equal(pr.Image, src.Image) {
		t.Errorf("Image = %v, want %v", pr.Image, src.Image)
	}
	if pr.Text != src.Text {
		t.Errorf("Text = %q, want %q", pr.Text, src.Text)
	}
	if pr.Domain != src.Domain {
		t.Errorf("Domain = %q, want %q", pr.Domain, src.Domain)
	}
	// InitFromPb does not receive a Uuid from the pb.Profile message, so it
	// should be left at its zero value rather than silently defaulting to
	// something from src.
	if pr.Uuid != "" {
		t.Errorf("Uuid = %q, want empty", pr.Uuid)
	}
}
