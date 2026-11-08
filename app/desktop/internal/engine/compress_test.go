// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Compress (CompressFiles) on the device: a file becomes a smaller copy and
// the original's bytes are deleted - the listing says original_removed,
// with original_hash the original's hash. Computers keep their originals
// (the owner's decision, 2026-11-08). The same cases, by name, as the Mac's
// SyncPathsTests.

// compress does to the file at path what the device's CompressFiles does:
// its content becomes copy, at newPath when the compress renamed it (a PNG
// saved as a JPEG), listed as a compressed copy of what was there.
func (d *fakeDevice) compress(path, newPath string, copy []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	original := sha(d.files[path])
	delete(d.files, path)
	delete(d.originals, path)
	if d.originals == nil {
		d.originals = map[string]string{}
	}
	d.files[newPath] = copy
	d.originals[newPath] = original
}

// traffic is what a pass asked the device that sends or changes a file.
type traffic struct{ has, begins, links, deletes, reads int }

func trafficOf(d *fakeDevice) traffic {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := traffic{begins: len(d.begins), links: len(d.links), deletes: len(d.deletes), reads: d.reads}
	for _, ev := range d.events {
		if ev == "has" {
			t.has++
		}
	}
	return t
}

// compressFixture: a two-way folder of files (name -> content) synced with
// a fake device that holds the same, after its first pass.
func compressFixture(t *testing.T, files map[string]string) (*Engine, *fakeDevice, config.RemoteFolder, string) {
	t.Helper()
	withConfigDir(t)
	device := map[string][]byte{}
	for name, content := range files {
		device["/pc/docs/"+name] = []byte(content)
	}
	d := storedDevice(device)
	local := t.TempDir()
	writeFiles(t, local, files)
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	if rec := e.lastSynced[f.ID]; len(rec) != len(files) {
		t.Fatalf("not in sync to begin with: %v", rec)
	}
	return e, d, f, local
}

// restarted is another engine over e's configuration and records, as
// after a restart.
func restarted(e *Engine, f config.RemoteFolder) *Engine {
	e2 := New(&config.Config{Domain: "dev", RemoteFolders: []config.RemoteFolder{f}}, "pw", nil)
	e2.ws = e.ws
	return e2
}

func readLocal(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("%s here: %v", name, err)
	}
	return string(b)
}

// rewrite gives the file here new content, and a modification time the
// hash cache can't miss.
func rewrite(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
}

func deviceHas(d *fakeDevice, path string) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.files[path]
	return b, ok
}

const (
	video4K   = "a 4K video, as recorded"
	video1080 = "the same video at 1080p"
	screenPNG = "a PNG screenshot"
	screenJPG = "the screenshot as a JPEG"
)

func TestCompressedRecordEntry(t *testing.T) {
	if got := compressedEntry("O", "D"); got != "compressed:O:D" {
		t.Fatalf("entry %q", got)
	}
	if l, r := recordBaselines("compressed:O:D"); l != "O" || r != "D" {
		t.Fatalf("baselines %q %q", l, r)
	}
	for _, bad := range []string{"compressed:O", "compressed::D", "compressed:O:", "compressed:"} {
		if l, r := recordBaselines(bad); l != "" || r != "" {
			t.Errorf("%q: %q %q, want no baseline", bad, l, r)
		}
	}
	cases := []struct{ local, remote, want string }{
		{"O", "D", "compressed:O:D"},
		{"O", "O", "O"},
		{"", "D", keptOnDevice("D")},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := recordEntry(c.local, c.remote); got != c.want {
			t.Errorf("recordEntry(%q, %q) = %q, want %q", c.local, c.remote, got, c.want)
		}
		if c.want == "" {
			continue
		}
		if l, r := recordBaselines(c.want); l != c.local || r != c.remote {
			t.Errorf("%q reads back as %q %q", c.want, l, r)
		}
	}
}

func TestBaselinesForACompressedCopy(t *testing.T) {
	copyOf := func(hash, original string) *pb.File {
		return &pb.File{Hash: hash, OriginalHash: original, OriginalRemoved: true}
	}
	compressed := copyOf("D", "O")
	cases := []struct {
		name          string
		entry         string
		f             *pb.File
		here          string
		local, remote string
	}{
		{"compressed since the last pass", "O", compressed, "O", "O", "D"},
		{"first pass", "", compressed, "O", "O", "D"},
		{"the original put back here", "X", compressed, "O", "O", "D"},
		{"changed here since", "O", compressed, "L2", "O", "D"},
		{"deleted here since", "O", compressed, "", "O", "D"},
		{"new on the device", "", compressed, "", "", ""},
		{"another file here", "X", compressed, "Y", "X", "X"},
		{"the pair as recorded", compressedEntry("O", "D"), compressed, "O", "O", "D"},
		{"the copy edited since", compressedEntry("O", "D"), copyOf("E", "O"), "O", "O", "D"},
		{"the copy deleted since", compressedEntry("O", "D"), nil, "O", "O", "D"},
		{"an edit keeps its original", "O", &pb.File{Hash: "E", OriginalHash: "O"}, "O", "O", "O"},
		{"kept on the device", keptOnDevice("D"), &pb.File{Hash: "D", OriginalHash: "O", OriginalRemoved: true, UploadOnly: true}, "", "", "D"},
	}
	for _, c := range cases {
		if l, r := baselinesFor(c.entry, c.f, c.here); l != c.local || r != c.remote {
			t.Errorf("%s: %q %q, want %q %q", c.name, l, r, c.local, c.remote)
		}
	}
}

func TestCompressedStems(t *testing.T) {
	stems := map[string]string{
		"a/IMG_1.HEIC":          "a/IMG_1",
		"x.tar.gz":              "x.tar",
		".profile":              ".profile",
		"a/.profile":            "a/.profile",
		"notes":                 "notes",
		"a.b/notes":             "a.b/notes",
		"my file.final version": "my file.final version",
		"x.":                    "x",
	}
	for key, want := range stems {
		if got := stemOf(key); got != want {
			t.Errorf("stemOf(%q) = %q, want %q", key, got, want)
		}
	}
	originals := map[string][]string{
		"x (2)":     {"x (2)", "x"},
		"a/x (12)":  {"a/x (12)", "a/x"},
		"x (1)":     {"x (1)"},
		"x (02)":    {"x (02)"},
		"x ()":      {"x ()"},
		"x (a)":     {"x (a)"},
		" (2)":      {" (2)"},
		"x(2)":      {"x(2)"},
		"x (2) (3)": {"x (2) (3)", "x (2)"},
		"x":         {"x"},
	}
	for s, want := range originals {
		if got := originalStems(s); !slices.Equal(got, want) {
			t.Errorf("originalStems(%q) = %q, want %q", s, got, want)
		}
	}
	byStem := originalsByStem(map[string]string{"a/IMG_1.JPG": "h1", "b (2).jpg": "h2", "c.txt": ""})
	want := map[string][]string{"a/IMG_1": {"h1"}, "b (2)": {"h2"}, "b": {"h2"}}
	if !maps.EqualFunc(byStem, want, slices.Equal) {
		t.Fatalf("originalsByStem: %v", byStem)
	}
}

func TestRenamedOriginals(t *testing.T) {
	jpg := &pb.File{Path: "/d/x.jpg", Hash: "D", OriginalHash: "O", OriginalRemoved: true}
	cases := []struct {
		name   string
		remote map[string]*pb.File
		here   []string
		hashes map[string]string
		record map[string]string
		want   map[string]string
	}{
		{"the original here", map[string]*pb.File{"x.jpg": jpg}, []string{"x.png"}, map[string]string{"x.png": "O"}, nil, map[string]string{"x.png": "x.jpg"}},
		{"changed here since", map[string]*pb.File{"x.jpg": jpg}, []string{"x.png"}, map[string]string{"x.png": "L2"}, map[string]string{"x.png": "O"}, map[string]string{"x.png": "x.jpg"}},
		{"deleted here since", map[string]*pb.File{"x.jpg": jpg}, nil, nil, map[string]string{"x.png": "O"}, map[string]string{"x.png": "x.jpg"}},
		{"the pair as recorded", map[string]*pb.File{"x.jpg": jpg}, []string{"x.png"}, map[string]string{"x.png": "L2"}, map[string]string{"x.png": compressedEntry("O", "D")}, map[string]string{"x.png": "x.jpg"}},
		{"the copy edited since", map[string]*pb.File{"x.jpg": jpg}, []string{"x.png"}, map[string]string{"x.png": "O"}, map[string]string{"x.png": compressedEntry("O", "E")}, map[string]string{}},
		{"another file here", map[string]*pb.File{"x.jpg": jpg}, []string{"x.png"}, map[string]string{"x.png": "Z"}, map[string]string{"x.png": "Z"}, map[string]string{}},
		{"its name taken", map[string]*pb.File{"x.jpg": {Hash: "J"}, "x (2).jpg": jpg}, []string{"x.png", "x.jpg"}, map[string]string{"x.png": "O", "x.jpg": "J"}, map[string]string{"x.jpg": "J"}, map[string]string{"x.png": "x (2).jpg"}},
		{"the copy here too", map[string]*pb.File{"x.jpg": jpg}, []string{"x.png", "x.jpg"}, map[string]string{"x.png": "O", "x.jpg": "D"}, nil, map[string]string{}},
		{"the copy synced already", map[string]*pb.File{"x.jpg": jpg}, []string{"x.png"}, map[string]string{"x.png": "O"}, map[string]string{"x.jpg": "D"}, map[string]string{}},
		{"the device has the name too", map[string]*pb.File{"x.jpg": jpg, "x.png": {Hash: "O"}}, []string{"x.png"}, map[string]string{"x.png": "O"}, nil, map[string]string{}},
		{"another folder", map[string]*pb.File{"b/x.jpg": jpg}, []string{"a/x.png"}, map[string]string{"a/x.png": "O"}, nil, map[string]string{}},
		{"kept on the device", map[string]*pb.File{"x.jpg": jpg}, nil, nil, map[string]string{"x.png": keptOnDevice("O")}, map[string]string{}},
		{"an edit, its original kept", map[string]*pb.File{"x.jpg": {Hash: "D", OriginalHash: "O"}}, []string{"x.png"}, map[string]string{"x.png": "O"}, nil, map[string]string{}},
		{"the first by name", map[string]*pb.File{"x.jpg": jpg}, []string{"x.webp", "x.png"}, map[string]string{"x.png": "O", "x.webp": "O"}, nil, map[string]string{"x.png": "x.jpg"}},
		{"the best match first", map[string]*pb.File{"x.jpg": jpg}, []string{"x.webp", "x.png"}, map[string]string{"x.png": "O", "x.webp": "O"}, map[string]string{"x.webp": compressedEntry("O", "D")}, map[string]string{"x.webp": "x.jpg"}},
	}
	for _, c := range cases {
		here := map[string]string{}
		for _, k := range c.here {
			here[k] = "/local/" + k
		}
		got := renamedOriginals(c.remote, here, c.hashes, c.record)
		if got == nil {
			got = map[string]string{}
		}
		if !maps.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// What the pass hashes even when new, for that.
	stems := renamedStems(map[string]*pb.File{"x (2).jpg": jpg, "y.jpg": {Hash: "Y"}}, map[string]string{}, nil)
	if !maps.Equal(stems, map[string]bool{"x (2)": true, "x": true}) {
		t.Fatalf("renamedStems: %v", stems)
	}
}

func TestCompressedCopies(t *testing.T) {
	remote := map[string]*pb.File{
		"b.mov": {Hash: "D1", OriginalHash: "O", OriginalRemoved: true},
		"a.mov": {Hash: "D2", OriginalHash: "O", OriginalRemoved: true},
		"c.mov": {Hash: "D3", OriginalHash: "P", OriginalRemoved: true},
		"e.jpg": {Hash: "E", OriginalHash: "Q"},
		"f.txt": {Hash: "F"},
	}
	if got := compressedCopies(remote); !maps.Equal(got, map[string]string{"O": "D2", "P": "D3"}) {
		t.Fatalf("copies %v", got)
	}
	here := map[string]bool{"O": true, "Q": true}
	withCompressedCopies(here, remote)
	if !maps.Equal(here, map[string]bool{"O": true, "Q": true, "D1": true, "D2": true}) {
		t.Fatalf("here with the copies: %v", here)
	}
	moved := movedOnDevice(map[string]*pb.File{"Archive/a.mov": remote["a.mov"], "f.txt": remote["f.txt"]}, map[string]string{"a.mov": compressedEntry("O", "D2"), "f.txt": "F"})
	if !maps.Equal(moved, map[string]bool{"D2": true, "O": true}) {
		t.Fatalf("moved %v", moved)
	}
}

// What the record says is here is a compressed file's original, not its
// copy: a folder whose files are mostly compressed is still the synced one.
func TestJudgeRootWithCompressedFiles(t *testing.T) {
	record := map[string]string{"a": compressedEntry("h1", "d1"), "b": "h2", "c": "h3", "k": keptOnDevice("h4")}
	if v := judgeRoot("t", "", record, map[string]string{"a": "h1", "b": "h2"}, nil); !v.ok {
		t.Fatal("the originals here weren't counted")
	}
	if v := judgeRoot("t", "", record, map[string]string{"a": "d1", "b": "h2"}, nil); v.ok {
		t.Fatal("the device's copy counted as the file here")
	}
}

// As SyncPathsTests' compressedCopyIsTheSameFile.
func TestTwoWayKeepsTheOriginalOfACompressedFile(t *testing.T) {
	e, d, f, local := compressFixture(t, map[string]string{"clip.mov": video4K, "notes.txt": "notes"})
	d.compress("/pc/docs/clip.mov", "/pc/docs/clip.mov", []byte(video1080))
	before := trafficOf(d)

	e.reconcileRemoteFolder(f)
	e.reconcileRemoteFolder(f)
	e2 := restarted(e, f)
	e2.reconcileRemoteFolder(f)

	if got := trafficOf(d); got != before {
		t.Fatalf("the device was asked %+v, before %+v: nothing should be sent or fetched", got, before)
	}
	if got := readLocal(t, local, "clip.mov"); got != video4K {
		t.Fatalf("clip.mov here: %q", got)
	}
	want := compressedEntry(sha([]byte(video4K)), sha([]byte(video1080)))
	for _, en := range []*Engine{e, e2} {
		if rec := en.lastSynced[f.ID]["clip.mov"]; rec != want {
			t.Fatalf("record %q, want %q", rec, want)
		}
	}
}

// As SyncPathsTests' compressedCopyOnAFirstPass: no record yet (a folder
// added again), the original here is the device's copy all the same.
func TestTwoWayCompressedFirstPass(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/docs/clip.mov": []byte(video1080)})
	d.originals = map[string]string{"/pc/docs/clip.mov": sha([]byte(video4K))}
	local := t.TempDir()
	writeFiles(t, local, map[string]string{"clip.mov": video4K})
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)

	e.reconcileRemoteFolder(f)
	e.reconcileRemoteFolder(f)

	if got := trafficOf(d); got != (traffic{}) {
		t.Fatalf("the device was asked %+v", got)
	}
	if got := readLocal(t, local, "clip.mov"); got != video4K || len(conflictFiles(t, local)) != 0 {
		t.Fatalf("clip.mov here %q, conflicts %v", got, conflictFiles(t, local))
	}
	if rec := e.lastSynced[f.ID]["clip.mov"]; rec != compressedEntry(sha([]byte(video4K)), sha([]byte(video1080))) {
		t.Fatalf("record %q", rec)
	}
}

// As SyncPathsTests' compressedCopyLaterChanges: what changes afterwards
// syncs as usual.
func TestTwoWayCompressedLaterChanges(t *testing.T) {
	setup := func(t *testing.T) (*Engine, *fakeDevice, config.RemoteFolder, string) {
		e, d, f, local := compressFixture(t, map[string]string{"clip.mov": video4K})
		d.compress("/pc/docs/clip.mov", "/pc/docs/clip.mov", []byte(video1080))
		e.reconcileRemoteFolder(f)
		return e, d, f, local
	}
	t.Run("changed here", func(t *testing.T) {
		e, d, f, local := setup(t)
		rewrite(t, local, "clip.mov", "the video, trimmed here")

		e.reconcileRemoteFolder(f)

		if got, _ := deviceHas(d, "/pc/docs/clip.mov"); string(got) != "the video, trimmed here" {
			t.Fatalf("the device has %q: the new version replaces the copy", got)
		}
		if rec := e.lastSynced[f.ID]["clip.mov"]; rec != sha([]byte("the video, trimmed here")) {
			t.Fatalf("record %q", rec)
		}
	})
	t.Run("edited on the device", func(t *testing.T) {
		e, d, f, local := setup(t)
		d.mu.Lock()
		d.files["/pc/docs/clip.mov"] = []byte("the copy, cut on a phone") // still a copy of the original
		d.mu.Unlock()

		e.reconcileRemoteFolder(f)

		if got := readLocal(t, local, "clip.mov"); got != "the copy, cut on a phone" {
			t.Fatalf("clip.mov here: %q", got)
		}
		if rec := e.lastSynced[f.ID]["clip.mov"]; rec != sha([]byte("the copy, cut on a phone")) {
			t.Fatalf("record %q", rec)
		}
	})
	t.Run("deleted here", func(t *testing.T) {
		e, d, f, local := setup(t)
		_ = os.Remove(filepath.Join(local, "clip.mov"))

		e.reconcileRemoteFolder(f)

		if _, ok := deviceHas(d, "/pc/docs/clip.mov"); ok {
			t.Fatal("still on the device")
		}
		if _, ok := e.lastSynced[f.ID]["clip.mov"]; ok {
			t.Fatal("still in the record")
		}
	})
	t.Run("deleted on the device", func(t *testing.T) {
		e, d, f, local := setup(t)
		d.mu.Lock()
		delete(d.files, "/pc/docs/clip.mov")
		d.mu.Unlock()

		e.reconcileRemoteFolder(f)

		if fileExists(filepath.Join(local, "clip.mov")) {
			t.Fatal("still here")
		}
		if got, err := os.ReadFile(trashedAt(t, local, "clip.mov")); err != nil || string(got) != video4K {
			t.Fatalf("not in the trash: %q %v", got, err)
		}
	})
}

// As SyncPathsTests' renamedCopyIsTheSameFile: the device saved the copy
// under another name (a PNG as x.jpg; "x (2).jpg" when x.jpg is taken).
// The original keeps its name here, the copy isn't downloaded, nothing is
// sent, and the record keeps the pair at the name here.
func TestTwoWayKeepsTheOriginalOfARenamedCopy(t *testing.T) {
	for _, c := range []struct{ name, copyName string }{{"renamed", "shot.jpg"}, {"its name taken", "shot (2).jpg"}} {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"shot.png": screenPNG}
			if c.copyName != "shot.jpg" {
				files["shot.jpg"] = "another photo"
			}
			e, d, f, local := compressFixture(t, files)
			d.compress("/pc/docs/shot.png", "/pc/docs/"+c.copyName, []byte(screenJPG))
			before := trafficOf(d)

			e.reconcileRemoteFolder(f)
			e.reconcileRemoteFolder(f)
			e2 := restarted(e, f)
			e2.reconcileRemoteFolder(f)

			if got := trafficOf(d); got != before {
				t.Fatalf("the device was asked %+v, before %+v", got, before)
			}
			if got := readLocal(t, local, "shot.png"); got != screenPNG {
				t.Fatalf("shot.png here: %q", got)
			}
			if fileExists(filepath.Join(local, c.copyName)) {
				t.Fatalf("%s was downloaded", c.copyName)
			}
			if got, ok := deviceHas(d, "/pc/docs/"+c.copyName); !ok || string(got) != screenJPG {
				t.Fatalf("the device's copy: %q %v", got, ok)
			}
			if _, ok := deviceHas(d, "/pc/docs/shot.png"); ok {
				t.Fatal("shot.png was sent to the device again")
			}
			rec := e2.lastSynced[f.ID]
			if rec["shot.png"] != compressedEntry(sha([]byte(screenPNG)), sha([]byte(screenJPG))) {
				t.Fatalf("record %v", rec)
			}
			if _, ok := rec[c.copyName]; ok {
				t.Fatalf("the copy has an entry of its own: %v", rec)
			}
		})
	}
	t.Run("first pass", func(t *testing.T) {
		withConfigDir(t)
		d := storedDevice(map[string][]byte{"/pc/docs/shot.jpg": []byte(screenJPG)})
		d.originals = map[string]string{"/pc/docs/shot.jpg": sha([]byte(screenPNG))}
		local := t.TempDir()
		writeFiles(t, local, map[string]string{"shot.png": screenPNG})
		f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
		e := twoWayEngine(t, d, "dev", f)

		e.reconcileRemoteFolder(f)
		e.reconcileRemoteFolder(f)

		if got := trafficOf(d); got != (traffic{}) {
			t.Fatalf("the device was asked %+v", got)
		}
		if fileExists(filepath.Join(local, "shot.jpg")) || readLocal(t, local, "shot.png") != screenPNG {
			t.Fatal("the copy came down, or the original changed")
		}
	})
}

// As SyncPathsTests' renamedCopyLaterChanges.
func TestTwoWayRenamedCopyLaterChanges(t *testing.T) {
	setup := func(t *testing.T) (*Engine, *fakeDevice, config.RemoteFolder, string) {
		e, d, f, local := compressFixture(t, map[string]string{"shot.png": screenPNG})
		d.compress("/pc/docs/shot.png", "/pc/docs/shot.jpg", []byte(screenJPG))
		e.reconcileRemoteFolder(f)
		return e, d, f, local
	}
	t.Run("changed here", func(t *testing.T) {
		e, d, f, local := setup(t)
		rewrite(t, local, "shot.png", "the screenshot, annotated")
		d.mu.Lock()
		mark := len(d.events)
		d.mu.Unlock()

		e.reconcileRemoteFolder(f)

		if _, ok := deviceHas(d, "/pc/docs/shot.jpg"); ok {
			t.Fatal("the old copy is still on the device")
		}
		if got, _ := deviceHas(d, "/pc/docs/shot.png"); string(got) != "the screenshot, annotated" {
			t.Fatalf("the device's shot.png: %q", got)
		}
		d.mu.Lock()
		events := d.events[mark:]
		d.mu.Unlock()
		if del, has := slices.Index(events, "delete"), slices.Index(events, "has"); del < 0 || has < del {
			t.Fatalf("requests %v: the copy goes before the new version", events)
		}
		if fileExists(filepath.Join(local, "shot.jpg")) {
			t.Fatal("the copy came down")
		}
		rec := e.lastSynced[f.ID]
		if rec["shot.png"] != sha([]byte("the screenshot, annotated")) || len(rec) != 1 {
			t.Fatalf("record %v", rec)
		}
		before := trafficOf(d)
		e.reconcileRemoteFolder(f)
		if got := trafficOf(d); got != before {
			t.Fatalf("the next pass asked %+v", got)
		}
	})
	t.Run("deleted here", func(t *testing.T) {
		e, d, f, local := setup(t)
		_ = os.Remove(filepath.Join(local, "shot.png"))

		e.reconcileRemoteFolder(f)

		if _, ok := deviceHas(d, "/pc/docs/shot.jpg"); ok {
			t.Fatal("the copy is still on the device")
		}
		if rec := e.lastSynced[f.ID]; len(rec) != 0 {
			t.Fatalf("record %v", rec)
		}
	})
	t.Run("the copy edited on the device", func(t *testing.T) {
		e, d, f, local := setup(t)
		d.mu.Lock()
		d.files["/pc/docs/shot.jpg"] = []byte("the JPEG, cropped on a phone")
		d.mu.Unlock()

		e.reconcileRemoteFolder(f)

		if got := readLocal(t, local, "shot.jpg"); got != "the JPEG, cropped on a phone" {
			t.Fatalf("shot.jpg here: %q", got)
		}
		if fileExists(filepath.Join(local, "shot.png")) {
			t.Fatal("the original is still here: the device's file is the edit now")
		}
		if got, err := os.ReadFile(trashedAt(t, local, "shot.png")); err != nil || string(got) != screenPNG {
			t.Fatalf("the original isn't in the trash: %q %v", got, err)
		}
	})
	t.Run("the copy deleted on the device", func(t *testing.T) {
		e, d, f, local := setup(t)
		d.mu.Lock()
		delete(d.files, "/pc/docs/shot.jpg")
		d.mu.Unlock()

		e.reconcileRemoteFolder(f)

		if fileExists(filepath.Join(local, "shot.png")) || readsAsked(d) != 0 {
			t.Fatalf("still here %v, reads %d", fileExists(filepath.Join(local, "shot.png")), readsAsked(d))
		}
		if rec := e.lastSynced[f.ID]; len(rec) != 0 {
			t.Fatalf("record %v", rec)
		}
	})
	t.Run("changed here, upload only", func(t *testing.T) {
		e, d, f, local := setup(t)
		d.mu.Lock()
		d.delAnswer = "upload_only"
		d.mu.Unlock()
		rewrite(t, local, "shot.png", "the screenshot, annotated")

		e.reconcileRemoteFolder(f)
		e.reconcileRemoteFolder(f)

		if _, ok := deviceHas(d, "/pc/docs/shot.jpg"); !ok || deletesAsked(d) != 1 {
			t.Fatalf("the copy is gone, or its delete asked %d times", deletesAsked(d))
		}
		if got, _ := deviceHas(d, "/pc/docs/shot.png"); string(got) != "the screenshot, annotated" {
			t.Fatalf("the device's shot.png: %q", got)
		}
		if fileExists(filepath.Join(local, "shot.jpg")) {
			t.Fatal("the copy kept on the device came down")
		}
		if rec := e.lastSynced[f.ID]["shot.jpg"]; rec != keptOnDevice(sha([]byte(screenJPG))) {
			t.Fatalf("the copy's record %q", rec)
		}
	})
}

// As SyncPathsTests' compressedOriginalRenamedHere: renamed here, the
// original goes up as the device's copy, linked at the new name before the
// old name is deleted there - an upload of the original would be answered
// with the copy at its old name and store nothing, and the next pass would
// take the file for one deleted on the device.
func TestTwoWayRenamedHereLinksTheCopy(t *testing.T) {
	e, d, f, local := compressFixture(t, map[string]string{"clip.mov": video4K})
	d.compress("/pc/docs/clip.mov", "/pc/docs/clip.mov", []byte(video1080))
	e.reconcileRemoteFolder(f)
	if err := os.Rename(filepath.Join(local, "clip.mov"), filepath.Join(local, "holiday.mov")); err != nil {
		t.Fatal(err)
	}
	before := trafficOf(d)
	d.mu.Lock()
	mark := len(d.events)
	d.mu.Unlock()

	e.reconcileRemoteFolder(f)

	d.mu.Lock()
	events := slices.Clone(d.events[mark:])
	var links []string
	for _, l := range d.links {
		links = append(links, l.Path+" "+l.Hash)
	}
	d.mu.Unlock()
	if want := []string{"/pc/docs/holiday.mov " + sha([]byte(video1080))}; !slices.Equal(links, want) {
		t.Fatalf("linked %v, want %v", links, want)
	}
	if link, del := slices.Index(events, "link"), slices.Index(events, "delete"); link < 0 || del < link {
		t.Fatalf("requests %v: the copy is linked before its old name goes", events)
	}
	if got := trafficOf(d); got.begins != before.begins || got.has != before.has || got.reads != before.reads {
		t.Fatalf("sent or fetched: %+v, before %+v", got, before)
	}
	if _, ok := deviceHas(d, "/pc/docs/clip.mov"); ok {
		t.Fatal("the old name is still on the device")
	}
	if got := readLocal(t, local, "holiday.mov"); got != video4K {
		t.Fatalf("holiday.mov here: %q", got)
	}
	rec := e.lastSynced[f.ID]
	if rec["holiday.mov"] != compressedEntry(sha([]byte(video4K)), sha([]byte(video1080))) || len(rec) != 1 {
		t.Fatalf("record %v", rec)
	}
	before = trafficOf(d)
	e.reconcileRemoteFolder(f)
	if got := trafficOf(d); got != before || !fileExists(filepath.Join(local, "holiday.mov")) {
		t.Fatalf("the next pass asked %+v", got)
	}
}

// As SyncPathsTests' backupOfACompressedFile: a backup holds a file the
// device compressed - at its name, or the name the compress gave it - and
// never sends it again, from the pass or from the watcher. A copy of the
// original here goes up as the device's copy, once.
func TestBackupKeepsTheOriginalOfACompressedFile(t *testing.T) {
	e, d, f := backupFixture(t, 0)
	prefix := e.remotePathFor(f.Path) + "/"
	writeFiles(t, f.Path, map[string]string{
		"clip.mov": video4K, "shot.png": screenPNG, "pic.heic": "a 48 MP HEIC", "pic.jpg": "another photo", "copy of shot.png": screenPNG,
	})
	d.mu.Lock()
	d.listStored = true
	d.files[prefix+"clip.mov"] = []byte(video1080)
	d.files[prefix+"shot.jpg"] = []byte(screenJPG)
	d.files[prefix+"pic.jpg"] = []byte("another photo")
	d.files[prefix+"pic (2).jpg"] = []byte("the HEIC at 12 MP")
	d.originals = map[string]string{
		prefix + "clip.mov": sha([]byte(video4K)), prefix + "shot.jpg": sha([]byte(screenPNG)), prefix + "pic (2).jpg": sha([]byte("a 48 MP HEIC")),
	}
	d.mu.Unlock()

	e.reconcile(f)

	d.mu.Lock()
	begins := len(d.begins)
	var links []string
	for _, l := range d.links {
		links = append(links, l.Path+" "+l.Hash)
	}
	d.mu.Unlock()
	if begins != 0 {
		t.Fatalf("%d upload(s): nothing here is new to the device", begins)
	}
	if want := []string{prefix + "copy of shot.png " + sha([]byte(screenJPG))}; !slices.Equal(links, want) {
		t.Fatalf("linked %v, want %v", links, want)
	}
	for _, name := range []string{"shot.png", "pic.heic"} {
		if _, ok := deviceHas(d, prefix+name); ok {
			t.Errorf("%s, compressed on the device, was sent again", name)
		}
	}

	before := trafficOf(d)
	e.reconcile(f)
	for _, name := range []string{"clip.mov", "shot.png", "pic.heic", "copy of shot.png"} {
		e.processChangedPath(filepath.Join(f.Path, name), f)
	}
	if got := trafficOf(d); got != before {
		t.Fatalf("asked %+v again, before %+v", got, before)
	}

	// Changed here, it goes up as usual.
	rewrite(t, f.Path, "clip.mov", "the video, trimmed here")
	e.processChangedPath(filepath.Join(f.Path, "clip.mov"), f)
	if got, _ := deviceHas(d, prefix+"clip.mov"); string(got) != "the video, trimmed here" {
		t.Fatalf("the device's clip.mov: %q", got)
	}
}
