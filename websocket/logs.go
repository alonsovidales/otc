// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/alonsovidales/otc/cfg"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/wsframe"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// Settings > Logs: the device's own log and the update log, read live by
// the owner, copied, or sent to the project through the bridge.

const (
	cLogsMaxBytes   = 256 << 10
	cLogsWaitMax    = 25 * time.Second
	cLogsWaitStep   = 300 * time.Millisecond
	cUpdateLogPath  = "/var/log/otc-update/update.log"
	cSendAppBytes   = 192 << 10
	cSendUpdateTail = 64 << 10
)

// logPath is where source's log is: "app" (the device's own, [logger]
// log_file) or "update" (the root updater's).
func logPath(source string) (string, error) {
	switch source {
	case "", "app":
		if cfg.HasSection("logger") {
			if p := cfg.GetStr("logger", "log_file"); p != "" {
				return p, nil
			}
		}
		return "", errors.New("this device logs to its console only")
	case "update":
		return cUpdateLogPath, nil
	}
	return "", fmt.Errorf("unknown log %q", source)
}

// readLog answers GetLogs: up to max bytes of source's log from offset (-1:
// the last max), waiting up to wait for it to grow when there is nothing
// new yet. A log rotated since (smaller than offset) is read from its start.
func readLog(source string, offset int64, max int, wait time.Duration) (*pb.Logs, error) {
	path, err := logPath(source)
	if err != nil {
		return nil, err
	}
	if max <= 0 || max > cLogsMaxBytes {
		max = cLogsMaxBytes
	}
	if wait > cLogsWaitMax {
		wait = cLogsWaitMax
	}
	deadline := time.Now().Add(wait)
	for {
		fi, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return &pb.Logs{NextOffset: 0}, nil
		}
		if err != nil {
			return nil, err
		}
		size := fi.Size()
		if offset < 0 {
			offset = max64(0, size-int64(max))
		} else if offset > size {
			offset = 0 // rotated
		}
		if size > offset || wait <= 0 || time.Now().After(deadline) {
			return readLogRange(path, offset, size, max)
		}
		time.Sleep(cLogsWaitStep)
	}
}

func readLogRange(path string, offset, size int64, max int) (*pb.Logs, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n := min(size-offset, int64(max))
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	buf = buf[:got]
	// Whole lines only, so the next piece starts on one: a line still
	// being written waits for the next read.
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 && int64(got) == n {
		buf = buf[:i+1]
	}
	return &pb.Logs{
		Text:       strings.ToValidUTF8(string(buf), "�"),
		NextOffset: offset + int64(len(buf)),
		Size:       size,
	}, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// logsForSupport is what "Send to us" sends: a header naming the device and
// its version, then the end of both logs - gzip'd.
func (mg *Manager) logsForSupport() ([]byte, error) {
	var b bytes.Buffer
	_, domain, _ := mg.settings.Identity()
	version, _ := os.ReadFile("/etc/otc/version")
	fmt.Fprintf(&b, "device: %s\nversion: %s\nsent: %s\n", domain, strings.TrimSpace(string(version)), time.Now().UTC().Format(time.RFC3339))
	for _, part := range []struct {
		source string
		bytes  int
	}{{"app", cSendAppBytes}, {"update", cSendUpdateTail}} {
		fmt.Fprintf(&b, "\n===== %s log =====\n", part.source)
		if l, err := readLog(part.source, -1, part.bytes, 0); err != nil {
			fmt.Fprintf(&b, "(not available: %v)\n", err)
		} else {
			b.WriteString(l.Text)
		}
	}
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	if _, err := w.Write(b.Bytes()); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return z.Bytes(), nil
}

// sendLogsToBridge hands the logs to the bridge (BridgeSendLogs), which
// mails them to the project; authenticated by this device's secret, a
// one-off connection like releaseBridgeDomain's.
func (mg *Manager) sendLogsToBridge(note string) error {
	if !bridgeConfigured() {
		return errors.New("this device isn't on the bridge, so it can't send its logs - use Copy instead")
	}
	logs, err := mg.logsForSupport()
	if err != nil {
		return err
	}
	u := url.URL{Scheme: "wss", Host: cfg.GetStr("otc", "bridge-addr"), Path: "/ws"}
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	c, err := wsframe.Dial(u.String(), h)
	if err != nil {
		return fmt.Errorf("dialing bridge: %w", err)
	}
	defer c.Close()
	owner, domain, secret := mg.settings.Identity()
	if len(note) > 2000 {
		note = note[:2000]
	}
	b, err := proto.Marshal(&pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqBridgeSendLogs{
			ReqBridgeSendLogs: &pb.BridgeSendLogs{OwnerUuid: owner, Domain: domain, Secret: secret, Note: note, Logs: logs},
		},
	})
	if err != nil {
		return err
	}
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		return fmt.Errorf("writing to bridge: %w", err)
	}
	_, data, err := c.ReadMessage()
	if err != nil {
		return fmt.Errorf("reading from bridge: %w", err)
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		return err
	}
	if resp.Error {
		return errors.New(resp.ErrorMessage)
	}
	return nil
}
