// SPDX-License-Identifier: AGPL-3.0-or-later

// otc-demo-setup finishes the demo device (docs/demo-device.md, step 5)
// once its library is uploaded (otc-put) and processed, from the content
// directory's JSON files (docs/demo-device/content): the profile, a name for
// each person the device found - the one most of its photos list in
// manifest.json - and the collections and posts. A person no name fits
// (the dog's face, a stranger in the background) is deleted. Run it with
// demo mode still off.
//
//	echo "$PASSWORD" | otc-demo-setup -address ws://127.0.0.1:8080/ws ~/src/otc-demo-content
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
	pb "github.com/alonsovidales/otc/proto/generated"
)

type manifestEntry struct {
	Path   string   `json:"path"`
	People []string `json:"people"`
}

func main() {
	address := flag.String("address", "", "the device's address (ws://127.0.0.1:8080/ws)")
	dryRun := flag.Bool("n", false, "say what would be done, change nothing")
	flag.Parse()
	if *address == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: otc-demo-setup [-n] -address <ws-url> <content-dir>   (password on stdin)")
		os.Exit(2)
	}
	pw, _ := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	if err := run(*address, strings.TrimRight(string(pw), "\r\n"), flag.Arg(0), *dryRun); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// remote is a content path (library/...) as the device has it.
func remote(p string) string {
	return "/" + strings.TrimPrefix(filepath.ToSlash(p), "library/")
}

func readJSON(dir, name string, v any) error {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

type client struct{ c *wsclient.Client }

func (c client) do(build func(*pb.ReqEnvelope)) (*pb.RespEnvelope, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, err := c.c.Request(ctx, build)
	if err != nil {
		return nil, err
	}
	if resp.Error {
		return resp, errors.New(resp.ErrorMessage)
	}
	if a, ok := resp.Payload.(*pb.RespEnvelope_RespAck); ok && !a.RespAck.Ok {
		return resp, errors.New(a.RespAck.ErrorMsg)
	}
	return resp, nil
}

func run(address, password, dir string, dryRun bool) error {
	var manifest []manifestEntry
	if err := readJSON(dir, "manifest.json", &manifest); err != nil {
		var wrapped struct {
			Items []manifestEntry `json:"items"`
		}
		if err2 := readJSON(dir, "manifest.json", &wrapped); err2 != nil {
			return fmt.Errorf("manifest.json: %w", err)
		}
		manifest = wrapped.Items
	}
	peopleOf := map[string][]string{}
	for _, e := range manifest {
		peopleOf[remote(e.Path)] = e.People
	}
	var profile struct {
		Name        string `json:"display_name"`
		Description string `json:"description"`
		Avatar      string `json:"avatar"`
	}
	if err := readJSON(dir, "profile.json", &profile); err != nil {
		return err
	}
	var collections []struct {
		Name  string   `json:"name"`
		Paths []string `json:"paths"`
	}
	if err := readJSON(dir, "collections.json", &collections); err != nil {
		return err
	}
	var posts []struct {
		Caption string   `json:"caption"`
		Photos  []string `json:"photos"`
	}
	if err := readJSON(dir, "posts.json", &posts); err != nil {
		return err
	}

	wc := wsclient.New()
	wc.Configure(address, "otc-demo-setup", password)
	wc.Connect()
	defer wc.Disconnect()
	for deadline := time.Now().Add(time.Minute); !wc.IsConnected(); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			return errors.New("could not connect and sign in")
		}
	}
	c := client{wc}

	// The profile.
	avatar, err := os.ReadFile(filepath.Join(dir, profile.Avatar))
	if err != nil {
		return err
	}
	fmt.Printf("profile: %q\n", profile.Name)
	if !dryRun {
		if _, err := c.do(func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqSetProfile{ReqSetProfile: &pb.Profile{Name: profile.Name, Text: profile.Description, Image: avatar}}
		}); err != nil {
			return fmt.Errorf("profile: %w", err)
		}
	}

	// People: each one the name most of its photos list.
	resp, err := c.do(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListPeople{ReqListPeople: &pb.ListPeople{}}
	})
	if err != nil {
		return fmt.Errorf("people: %w", err)
	}
	people := resp.GetRespPeople().GetPeople()
	named := map[string]string{}
	for _, p := range people {
		paths, err := photosOf(c, p.Id)
		if err != nil {
			return fmt.Errorf("photos of %s: %w", p.Id, err)
		}
		votes := map[string]int{}
		for _, path := range paths {
			for _, n := range peopleOf[path] {
				votes[n]++
			}
		}
		best, bestN := "", 0
		for n, v := range votes {
			if v > bestN || (v == bestN && n < best) {
				best, bestN = n, v
			}
		}
		// A name fits when nearly every photo of the person lists it, and
		// the person isn't a stray few faces.
		if best != "" && len(paths) >= 5 && bestN*10 >= len(paths)*9 && named[best] == "" {
			named[best] = p.Id
			fmt.Printf("person %s (%d faces, %d photos): %s (%d of %d)\n", p.Id, p.FaceCount, len(paths), best, bestN, len(paths))
			if !dryRun {
				if _, err := c.do(func(r *pb.ReqEnvelope) {
					r.Payload = &pb.ReqEnvelope_ReqRenamePerson{ReqRenamePerson: &pb.RenamePerson{Id: p.Id, Name: best}}
				}); err != nil {
					return fmt.Errorf("naming %s: %w", best, err)
				}
			}
			continue
		}
		fmt.Printf("person %s (%d faces, %d photos, best %q %d): deleted\n", p.Id, p.FaceCount, len(paths), best, bestN)
		if !dryRun {
			if _, err := c.do(func(r *pb.ReqEnvelope) {
				r.Payload = &pb.ReqEnvelope_ReqDeletePerson{ReqDeletePerson: &pb.DeletePerson{Id: p.Id}}
			}); err != nil {
				return fmt.Errorf("deleting %s: %w", p.Id, err)
			}
		}
	}
	var missing []string
	for _, e := range manifest {
		for _, n := range e.People {
			if named[n] == "" && !contains(missing, n) {
				missing = append(missing, n)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		fmt.Printf("no person found for: %s\n", strings.Join(missing, ", "))
	}

	// Collections and posts.
	for _, col := range collections {
		paths := make([]string, len(col.Paths))
		for i, p := range col.Paths {
			paths[i] = remote(p)
		}
		fmt.Printf("collection %q: %d items\n", col.Name, len(paths))
		if !dryRun {
			if _, err := c.do(func(r *pb.ReqEnvelope) {
				r.Payload = &pb.ReqEnvelope_ReqCreateImageGroup{ReqCreateImageGroup: &pb.CreateImageGroup{Name: col.Name, Paths: paths}}
			}); err != nil {
				return fmt.Errorf("collection %q: %w", col.Name, err)
			}
		}
	}
	for _, post := range posts {
		paths := make([]string, len(post.Photos))
		for i, p := range post.Photos {
			paths[i] = remote(p)
		}
		fmt.Printf("post: %q (%d photos)\n", post.Caption, len(paths))
		if !dryRun {
			if _, err := c.do(func(r *pb.ReqEnvelope) {
				r.Payload = &pb.ReqEnvelope_ReqNewSocialPublication{ReqNewSocialPublication: &pb.NewSocialPublication{Text: post.Caption, Paths: paths}}
			}); err != nil {
				return fmt.Errorf("post: %w", err)
			}
		}
	}
	return nil
}

// photosOf is every photo of one person, page after page.
func photosOf(c client, id string) ([]string, error) {
	var paths []string
	token := ""
	for {
		resp, err := c.do(func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqSearchPhotos{ReqSearchPhotos: &pb.SearchPhotos{PersonIds: []string{id}, Token: token, OmitThumbnails: true}}
		})
		if err != nil {
			return nil, err
		}
		list := resp.GetRespListOfFiles()
		for _, f := range list.GetFiles() {
			paths = append(paths, f.Path)
		}
		if len(list.GetFiles()) == 0 || list.GetToken() == "" || list.GetToken() == token {
			return paths, nil
		}
		token = list.GetToken()
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
