// SPDX-License-Identifier: AGPL-3.0-or-later

// otc-put uploads a folder tree to a device at the path chosen for it,
// keeping each file's modification time as its date: how the demo device's
// sample library (docs/demo-device/content) is loaded, as /Photos,
// /Documents... rather than under a computer's name as otc-sync files a
// backup. Files already there with the same content are left alone; the
// device links the rest by hash when it has their content.
//
//	echo "$PASSWORD" | otc-put -address ws://127.0.0.1:8080/ws <dir> <remote-dir>
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// cChunk is the device's MaxChunk, as the sync engine sends.
const cChunk = 4 << 20

func main() {
	address := flag.String("address", "", "the device's address (wss://name.off-the.cloud/ws, ws://127.0.0.1:8080/ws)")
	flag.Parse()
	if *address == "" || flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: otc-put -address <ws-url> <dir> <remote-dir>   (password on stdin)")
		os.Exit(2)
	}
	pw, _ := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	password := strings.TrimRight(string(pw), "\r\n")
	if err := run(*address, password, flag.Arg(0), flag.Arg(1)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(address, password, dir, remoteDir string) error {
	c := wsclient.New()
	c.Configure(address, "otc-put", password)
	c.Connect()
	defer c.Disconnect()
	for deadline := time.Now().Add(time.Minute); !c.IsConnected(); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			return errors.New("could not connect and sign in")
		}
	}
	remoteDir = "/" + strings.Trim(remoteDir, "/")
	var sent, linked, failed int
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		remote := path.Join(remoteDir, filepath.ToSlash(rel))
		how, err := put(c, p, remote)
		switch {
		case err != nil:
			failed++
			fmt.Fprintf(os.Stderr, "%s: %v\n", remote, err)
		case how == "linked":
			linked++
		default:
			sent++
		}
		fmt.Printf("%s %s\n", how, remote)
		return nil
	})
	fmt.Printf("%d sent, %d linked, %d failed\n", sent, linked, failed)
	if err == nil && failed > 0 {
		err = fmt.Errorf("%d files failed", failed)
	}
	return err
}

func request(c *wsclient.Client, build func(*pb.ReqEnvelope)) (*pb.RespEnvelope, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return c.Request(ctx, build)
}

// put sends one file: linked when the device already has its content.
func put(c *wsclient.Client, p, remote string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	hash := hex.EncodeToString(h.Sum(nil))
	when := timestamppb.New(st.ModTime())

	has, err := request(c, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqHasFile{ReqHasFile: &pb.HasFile{Hash: hash}}
	})
	if err != nil {
		return "", err
	}
	if fe, ok := has.Payload.(*pb.RespEnvelope_RespFileExists); ok && fe.RespFileExists.Exists {
		resp, err := request(c, func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqLinkFile{ReqLinkFile: &pb.LinkFile{Hash: hash, Path: remote, ForceOverride: true, Created: when, Modified: when}}
		})
		if err != nil {
			return "", err
		}
		return "linked", wsclient.RespError(resp, "link rejected")
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	resp, err := request(c, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqBeginUpload{ReqBeginUpload: &pb.BeginUpload{
			Path: remote, Size: st.Size(), Created: when, Modified: when, ForceOverride: true,
		}}
	})
	if err != nil {
		return "", err
	}
	if err := wsclient.RespError(resp, "upload rejected"); err != nil {
		return "", err
	}
	started, ok := resp.Payload.(*pb.RespEnvelope_RespUploadStarted)
	if !ok {
		return "", errors.New("unexpected response to BeginUpload")
	}
	id := started.RespUploadStarted.UploadId
	buf := make([]byte, cChunk)
	for offset := int64(0); offset < st.Size(); {
		n, err := io.ReadFull(f, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return "", err
		}
		if n == 0 {
			return "", errors.New("the file shrank while it was being sent")
		}
		chunk, at := buf[:n], offset
		resp, err := request(c, func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqUploadChunk{ReqUploadChunk: &pb.UploadChunk{UploadId: id, Offset: at, Data: chunk}}
		})
		if err != nil {
			return "", err
		}
		if err := wsclient.RespError(resp, "upload rejected"); err != nil {
			return "", err
		}
		offset += int64(n)
	}
	resp, err = request(c, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqFinishUpload{ReqFinishUpload: &pb.FinishUpload{UploadId: id, Sha256: hash}}
	})
	if err != nil {
		return "", err
	}
	return "sent", wsclient.RespError(resp, "upload rejected")
}
