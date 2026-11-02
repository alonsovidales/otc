// SPDX-License-Identifier: AGPL-3.0-or-later

// otc-demo-setup finishes the demo device (docs/demo-device.md, step 5)
// once its library is uploaded (otc-put) and processed, from the content
// directory's JSON files (docs/demo-device/content): Places turned on (the
// map in Images, off on every other device until its user turns it on;
// the demo's photos are sample photos), the profile, a name for each
// person the device found - the one most of its photos list in
// manifest.json - and the collections and posts. A person no name fits
// (the dog's face, a stranger in the background) is deleted. Run it with
// demo mode still off. -places-only turns Places on and stops: for a demo
// device set up before Places. Smart search (beta) is turned on too - a
// device without its model downloads it first (release 152), then
// describes the photos; -smart-search-only turns it on and stops (a demo
// set up before it). The photos in favourites.json go in Favourites;
// -favourites-only does just that (a demo set up before Favourites).
// ratings.json, when there is one, turns Rating on for a collection (release
// 163): the owner's stars and comments, then a rated shared gallery of it
// through which its "link" entries are rated as a client would - they show
// as from the shared link, and the device raises its Rating alert; the
// link lasts 30 days, and one made before with the same description is
// deleted first. -rating-only does just that (a demo set up before Rating).
//
//	echo "$PASSWORD" | otc-demo-setup -address ws://127.0.0.1:8080/ws ~/src/otc-demo-content
//	echo "$PASSWORD" | otc-demo-setup -places-only -address ws://127.0.0.1:8080/ws
//	echo "$PASSWORD" | otc-demo-setup -smart-search-only -address ws://127.0.0.1:8080/ws
//	echo "$PASSWORD" | otc-demo-setup -favourites-only -address ws://127.0.0.1:8080/ws ~/src/otc-demo-content
//	echo "$PASSWORD" | otc-demo-setup -rating-only -address ws://127.0.0.1:8080/ws ~/src/otc-demo-content
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

type manifestEntry struct {
	Path   string   `json:"path"`
	People []string `json:"people"`
}

func main() {
	address := flag.String("address", "", "the device's address (ws://127.0.0.1:8080/ws)")
	dryRun := flag.Bool("n", false, "say what would be done, change nothing")
	placesOnly := flag.Bool("places-only", false, "turn Places on and stop (a demo set up before Places)")
	smartOnly := flag.Bool("smart-search-only", false, "turn Smart search on and stop (a demo set up before it)")
	favouritesOnly := flag.Bool("favourites-only", false, "put favourites.json's photos in Favourites and stop (a demo set up before it)")
	ratingOnly := flag.Bool("rating-only", false, "rate ratings.json's collection, through its link too, and stop (a demo set up before Rating)")
	flag.Parse()
	if *address == "" || (flag.NArg() != 1 && !*placesOnly && !*smartOnly) {
		fmt.Fprintln(os.Stderr, "usage: otc-demo-setup [-n] -address <ws-url> <content-dir>   (password on stdin)")
		fmt.Fprintln(os.Stderr, "       otc-demo-setup [-n] -places-only -address <ws-url>")
		fmt.Fprintln(os.Stderr, "       otc-demo-setup [-n] -smart-search-only -address <ws-url>")
		fmt.Fprintln(os.Stderr, "       otc-demo-setup [-n] -favourites-only -address <ws-url> <content-dir>")
		fmt.Fprintln(os.Stderr, "       otc-demo-setup [-n] -rating-only -address <ws-url> <content-dir>")
		os.Exit(2)
	}
	pw, _ := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	password := strings.TrimRight(string(pw), "\r\n")
	if *favouritesOnly || *ratingOnly {
		c, done, err := signIn(*address, password)
		if err == nil {
			if *favouritesOnly {
				err = favouritesFrom(c, flag.Arg(0), *dryRun)
			} else {
				err = ratingFrom(c, *address, flag.Arg(0), *dryRun)
			}
			done()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if *placesOnly || *smartOnly {
		c, done, err := signIn(*address, password)
		if err == nil {
			if *placesOnly {
				err = placesOn(c, *dryRun)
			} else {
				err = smartSearchOn(c, *dryRun)
			}
			done()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*address, password, flag.Arg(0), *dryRun); err != nil {
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

// signIn connects to the device at address and signs in; done
// disconnects.
func signIn(address, password string) (c client, done func(), err error) {
	wc := wsclient.New()
	wc.Configure(address, "otc-demo-setup", password)
	wc.Connect()
	for deadline := time.Now().Add(time.Minute); !wc.IsConnected(); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			wc.Disconnect()
			return client{}, nil, errors.New("could not connect and sign in")
		}
	}
	return client{wc}, wc.Disconnect, nil
}

// placesOn turns Places on: the demo shows its photos on the map, and the
// device reads where they were taken in the background.
func placesOn(c client, dryRun bool) error {
	fmt.Println("places: on")
	if dryRun {
		return nil
	}
	if _, err := c.do(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqSetPlacesEnabled{ReqSetPlacesEnabled: &pb.SetPlacesEnabled{Enabled: true}}
	}); err != nil {
		return fmt.Errorf("places: %w", err)
	}
	return nil
}

// smartSearchOn turns Smart search (beta) on: the demo's photos can be
// found by describing them, once the device has described them in the
// background - after downloading its model when it hasn't it. A device
// that can't get the model only says so.
func smartSearchOn(c client, dryRun bool) error {
	fmt.Println("smart search: on")
	if dryRun {
		return nil
	}
	resp, err := c.do(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqSetSmartSearchEnabled{ReqSetSmartSearchEnabled: &pb.SetSmartSearchEnabled{Enabled: true}}
	})
	if err != nil && resp != nil && resp.ErrorCode == "smart_search_unavailable" {
		fmt.Println("smart search: the device can't get its model:", err)
		return nil
	}
	if err != nil {
		return fmt.Errorf("smart search: %w", err)
	}
	resp, err = c.do(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqGetSettings{ReqGetSettings: &pb.GetSettings{}}
	})
	if err == nil {
		if m := resp.GetRespSettings().GetSmartSearchModel(); m.GetDownloading() {
			fmt.Printf("smart search: the device is downloading its model first (%d%%), then describes the photos\n", m.GetPercent())
		}
	}
	return nil
}

// favouritesFrom puts the photos favourites.json lists in Favourites, the
// collection every device has.
func favouritesFrom(c client, dir string, dryRun bool) error {
	var favourites []string
	if err := readJSON(dir, "favourites.json", &favourites); err != nil {
		return err
	}
	paths := make([]string, len(favourites))
	for i, p := range favourites {
		paths[i] = remote(p)
	}
	fmt.Printf("favourites: %d photos\n", len(paths))
	if dryRun {
		return nil
	}
	if _, err := c.do(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqSetFavourite{ReqSetFavourite: &pb.SetFavourite{Paths: paths, Favourite: true}}
	}); err != nil {
		return fmt.Errorf("favourites: %w", err)
	}
	return nil
}

type ratingEntry struct {
	Path    string `json:"path"`
	Stars   int32  `json:"stars"`
	Comment string `json:"comment"`
}

// ratingFrom turns Rating on for ratings.json's collection, rates it as the
// owner, then makes a rated shared gallery of it and rates the "link"
// entries through that, as a client opening the link would. Without
// ratings.json it does nothing.
func ratingFrom(c client, address, dir string, dryRun bool) error {
	var ratings struct {
		Collection      string        `json:"collection"`
		LinkDescription string        `json:"link_description"`
		Owner           []ratingEntry `json:"owner"`
		Link            []ratingEntry `json:"link"`
	}
	if err := readJSON(dir, "ratings.json", &ratings); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("rating: no ratings.json, nothing rated")
			return nil
		}
		return err
	}
	fmt.Printf("rating: %q, %d rated by the owner, %d through its link\n", ratings.Collection, len(ratings.Owner), len(ratings.Link))
	if dryRun {
		return nil
	}
	resp, err := c.do(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListImageGroups{ReqListImageGroups: &pb.ListImageGroups{SmallThumbnails: true}}
	})
	if err != nil {
		return fmt.Errorf("rating: the collections: %w", err)
	}
	gid := ""
	for _, g := range resp.GetRespImageGroups().GetGroups() {
		if g.Name == ratings.Collection && !g.Favourites {
			gid = g.Id
		}
	}
	if gid == "" {
		return fmt.Errorf("rating: no collection %q", ratings.Collection)
	}
	if _, err := c.do(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqSetImageGroupRating{ReqSetImageGroupRating: &pb.SetImageGroupRating{Id: gid, On: true}}
	}); err != nil {
		return fmt.Errorf("rating: turning it on: %w", err)
	}
	for _, e := range ratings.Owner {
		if _, err := c.do(func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqSetPhotoRating{ReqSetPhotoRating: &pb.SetPhotoRating{GroupId: gid, Path: remote(e.Path), Stars: e.Stars, Comment: e.Comment}}
		}); err != nil {
			return fmt.Errorf("rating %s: %w", e.Path, err)
		}
	}
	if len(ratings.Link) == 0 {
		return nil
	}

	// The link: an earlier one of the same description goes first.
	resp, err = c.do(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListSharedLinks{ReqListSharedLinks: &pb.ListSharedLinks{}}
	})
	if err != nil {
		return fmt.Errorf("rating: the shared links: %w", err)
	}
	for _, l := range resp.GetRespSharedLinks().GetLinks() {
		if l.Kind == "gallery" && l.Description == ratings.LinkDescription {
			if _, err := c.do(func(r *pb.ReqEnvelope) {
				r.Payload = &pb.ReqEnvelope_ReqDeleteSharedLink{ReqDeleteSharedLink: &pb.DeleteSharedLink{Uuid: l.Uuid}}
			}); err != nil {
				return fmt.Errorf("rating: deleting the earlier link: %w", err)
			}
		}
	}
	resp, err = c.do(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqCreateSharedGallery{ReqCreateSharedGallery: &pb.CreateSharedGallery{
			Source: &pb.SharedGallerySource{GroupId: gid}, Description: ratings.LinkDescription, TtlHours: 30 * 24, Rating: true}}
	})
	if err != nil {
		return fmt.Errorf("rating: the link: %w", err)
	}
	jobID := resp.GetRespSharedGalleryJob().GetJobId()
	link := ""
	for deadline := time.Now().Add(5 * time.Minute); link == ""; time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			return errors.New("rating: the link took too long")
		}
		resp, err := c.do(func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqGetSharedGalleryJob{ReqGetSharedGalleryJob: &pb.GetSharedGalleryJob{JobId: jobID}}
		})
		if err != nil {
			return fmt.Errorf("rating: the link: %w", err)
		}
		if j := resp.GetRespSharedGalleryJob(); j.GetFinished() {
			if j.GetError() != "" {
				return fmt.Errorf("rating: the link: %s", j.GetError())
			}
			link = j.GetLink()
		}
	}
	uuid, secret, ok := strings.Cut(link[strings.Index(link, "#")+1:], ".")
	if !ok {
		return errors.New("rating: the link has no secret")
	}

	// As the client: a connection that never signs in.
	v, err := dialVisitor(address)
	if err != nil {
		return err
	}
	defer v.c.Close()
	resp, err = v.ask(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqOpenSharedGallery{ReqOpenSharedGallery: &pb.OpenSharedGallery{Uuid: uuid, Secret: secret}}
	})
	if err != nil {
		return fmt.Errorf("rating: opening the link: %w", err)
	}
	index := map[string]int32{}
	for _, it := range resp.GetRespSharedGallery().GetItems() {
		if _, twice := index[it.Name]; twice {
			index[it.Name] = -1
		} else {
			index[it.Name] = it.Index
		}
	}
	for _, e := range ratings.Link {
		i, found := index[path.Base(remote(e.Path))]
		if !found || i < 0 {
			return fmt.Errorf("rating: %s isn't once in the link", e.Path)
		}
		resp, err := v.ask(func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqRateSharedGalleryItem{ReqRateSharedGalleryItem: &pb.RateSharedGalleryItem{Uuid: uuid, Secret: secret, Index: i, Stars: e.Stars, Comment: e.Comment}}
		})
		if err != nil {
			return fmt.Errorf("rating %s through the link: %w", e.Path, err)
		}
		if a := resp.GetRespAck(); a == nil || !a.Ok {
			return fmt.Errorf("rating %s through the link: %s", e.Path, a.GetErrorMsg())
		}
	}
	fmt.Printf("rating: %d rated through the link, which lasts 30 days\n", len(ratings.Link))
	return nil
}

// visitor is a connection to the device that never signs in, as a shared
// gallery's page has.
type visitor struct {
	c  *websocket.Conn
	id int32
}

func dialVisitor(address string) (*visitor, error) {
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	conn, _, err := websocket.DefaultDialer.Dial(address, h)
	if err != nil {
		return nil, fmt.Errorf("rating: connecting as the link's visitor: %w", err)
	}
	return &visitor{c: conn}, nil
}

func (v *visitor) ask(build func(*pb.ReqEnvelope)) (*pb.RespEnvelope, error) {
	v.id++
	req := &pb.ReqEnvelope{Id: v.id}
	build(req)
	b, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := v.c.WriteMessage(websocket.BinaryMessage, b); err != nil {
		return nil, err
	}
	for {
		v.c.SetReadDeadline(time.Now().Add(time.Minute))
		_, data, err := v.c.ReadMessage()
		if err != nil {
			return nil, err
		}
		var resp pb.RespEnvelope
		if err := proto.Unmarshal(data, &resp); err != nil {
			return nil, err
		}
		if resp.Id != v.id {
			continue
		}
		if resp.Error {
			return &resp, errors.New(resp.ErrorMessage)
		}
		return &resp, nil
	}
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

	c, done, err := signIn(address, password)
	if err != nil {
		return err
	}
	defer done()

	if err := placesOn(c, dryRun); err != nil {
		return err
	}
	if err := smartSearchOn(c, dryRun); err != nil {
		return err
	}

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

	// Favourites, collections and posts.
	if err := favouritesFrom(c, dir, dryRun); err != nil {
		return err
	}
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
	if err := ratingFrom(c, address, dir, dryRun); err != nil {
		return err
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
