// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseMeminfo(t *testing.T) {
	m, err := ParseMeminfo(`MemTotal:       65756160 kB
MemFree:         1200000 kB
MemAvailable:   50000000 kB
Buffers:          100000 kB
Cached:          2000000 kB
SwapTotal:       4194300 kB
SwapFree:        4194000 kB
HugePages_Total:       0
`)
	if err != nil {
		t.Fatal(err)
	}
	if m.Total != 65756160*1024 || m.Available != 50000000*1024 || m.SwapTotal != 4194300*1024 || m.SwapFree != 4194000*1024 {
		t.Fatalf("got %+v", m)
	}
	// Without MemAvailable (old kernels): free + buffers + cached.
	m, err = ParseMeminfo("MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 10 kB\nCached: 200 kB\n")
	if err != nil || m.Available != 310*1024 {
		t.Fatalf("fallback: %+v %v", m, err)
	}
	if _, err := ParseMeminfo("nonsense"); err == nil {
		t.Fatal("no MemTotal must be an error")
	}
}

func TestParseMdstatHealthy(t *testing.T) {
	r := ParseMdstat(`Personalities : [raid1] [linear] [multipath] [raid0] [raid6] [raid5] [raid4] [raid10]
md2 : active raid1 nvme0n1p3[0] nvme1n1p3[1]
      1872962560 blocks super 1.2 [2/2] [UU]
      bitmap: 3/14 pages [12KB], 65536KB chunk

md1 : active raid1 nvme1n1p2[1] nvme0n1p2[0]
      1046528 blocks super 1.2 [2/2] [UU]

unused devices: <none>
`)
	if !r.Present || len(r.Arrays) != 2 {
		t.Fatalf("got %+v", r)
	}
	a := r.Arrays[0]
	if a.Name != "md2" || a.Level != "raid1" || !a.Active || a.Devices != 2 || a.Want != 2 || a.Have != 2 || a.Status != "[UU]" || a.Failed != 0 || a.Sync != "" {
		t.Fatalf("md2: %+v", a)
	}
}

func TestParseMdstatDegradedRecoveringAndInactive(t *testing.T) {
	r := ParseMdstat(`Personalities : [raid1]
md1 : active raid1 sdb2[2] sda2[0](F)
      523712 blocks super 1.2 [2/1] [U_]
      [=>...................]  recovery =  8.9% (46592/523712) finish=0.6min speed=11648K/sec

md0 : active (auto-read-only) raid1 sdb1[1] sda1[0]
      1046528 blocks super 1.2 [2/2] [UU]
      	resync=PENDING

md127 : inactive sdc[0](S)
      976630488 blocks super 1.2

unused devices: <none>
`)
	if len(r.Arrays) != 3 {
		t.Fatalf("got %+v", r.Arrays)
	}
	md1, md0, md127 := r.Arrays[0], r.Arrays[1], r.Arrays[2]
	if md1.Failed != 1 || md1.Have != 1 || md1.Want != 2 || md1.Status != "[U_]" || md1.Sync != "recovery 8.9%" {
		t.Fatalf("md1: %+v", md1)
	}
	if md0.Level != "raid1" || md0.Sync != "resync pending" || md0.Devices != 2 {
		t.Fatalf("md0: %+v", md0)
	}
	if md127.Active || md127.Devices != 1 {
		t.Fatalf("md127: %+v", md127)
	}
}

func TestParseMdstatNoRAID(t *testing.T) {
	for _, s := range []string{"", "Personalities : \nunused devices: <none>\n"} {
		if r := ParseMdstat(s); r.Present || len(r.Arrays) != 0 {
			t.Fatalf("%q: %+v", s, r)
		}
	}
}

func TestParseLoadavgAndUptime(t *testing.T) {
	l, err := ParseLoadavg("0.52 0.58 0.59 2/1234 56789\n")
	if err != nil || l != [3]float64{0.52, 0.58, 0.59} {
		t.Fatalf("%v %v", l, err)
	}
	if _, err := ParseLoadavg("1 2"); err == nil {
		t.Fatal("short loadavg must fail")
	}
	u, err := ParseUptime("350735.47 234388.90\n")
	if err != nil || u != 350735 {
		t.Fatalf("%v %v", u, err)
	}
}

func TestCPUStatAndPercent(t *testing.T) {
	a, btime, err := ParseCPUStat("cpu  100 0 100 700 50 0 0 50 0 0\ncpu0 1 2 3\nbtime 1759900000\n")
	if err != nil || btime != 1759900000 {
		t.Fatal(err, btime)
	}
	if a.Total != 1000 || a.Idle != 700 || a.IOWait != 50 || a.Steal != 50 {
		t.Fatalf("%+v", a)
	}
	// 1000 more jiffies: 600 idle, 100 iowait, 100 steal, 200 user.
	b, _, _ := ParseCPUStat("cpu  300 0 100 1300 150 0 0 150 0 0\n")
	p := CPUPercent(a, b)
	if p == nil || p.Busy != 30 || p.IOWait != 10 || p.Steal != 10 {
		t.Fatalf("%+v", p)
	}
	if CPUPercent(b, b) != nil {
		t.Fatal("no time passed: no figure")
	}
}

func TestParseNetDevAndRates(t *testing.T) {
	const s1 = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 9999 10 0 0 0 0 0 0 9999 10 0 0 0 0 0 0
  eno1: 1000 10 1 0 0 0 0 0 2000 20 2 0 0 0 0 0
   wg0: 500 5 0 0 0 0 0 0 600 6 0 0 0 0 0 0
`
	const s2 = `
    lo: 99999 10 0 0 0 0 0 0 99999 10 0 0 0 0 0 0
  eno1: 31000 10 1 0 0 0 0 0 62000 20 2 0 0 0 0 0
   wg0: 500 5 0 0 0 0 0 0 600 6 0 0 0 0 0 0
`
	a, b := ParseNetDev(s1), ParseNetDev(s2)
	if _, ok := a["lo"]; ok {
		t.Fatal("loopback must be left out")
	}
	r := NetRates(a, b, 30)
	if len(r) != 2 || r[0].Name != "eno1" || r[0].RxBps != 1000 || r[0].TxBps != 2000 || r[0].RxErrs != 1 || r[0].TxErrs != 2 || r[1].RxBps != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestParseMounts(t *testing.T) {
	m := ParseMounts(`/dev/md2 / ext4 rw,relatime 0 0
proc /proc proc rw 0 0
tmpfs /run tmpfs rw 0 0
/dev/md1 /boot ext4 rw 0 0
/dev/nvme0n1p1 /boot/efi vfat rw 0 0
/dev/md2 /var/lib/docker ext4 rw 0 0
/dev/loop0 /snap/core22/1 squashfs ro 0 0
/dev/sdc1 /mnt/my\040disk xfs rw 0 0
/dev/loop5 /mnt/my\040disk ext4 rw 0 0
`)
	want := []Mount{
		{"/dev/md2", "/", "ext4"},
		{"/dev/md1", "/boot", "ext4"},
		{"/dev/nvme0n1p1", "/boot/efi", "vfat"},
		{"/dev/sdc1", "/mnt/my disk", "xfs"},
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("got %+v", m)
	}
}

func TestParseSmallOutputs(t *testing.T) {
	if os := ParseOSRelease("NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.3 LTS\"\n"); os != "Ubuntu 24.04.3 LTS" {
		t.Fatal(os)
	}
	if n, s, err := ParseAptCheck("12;5"); err != nil || n != 12 || s != 5 {
		t.Fatal(n, s, err)
	}
	if _, _, err := ParseAptCheck("E: something"); err == nil {
		t.Fatal("garbage must fail")
	}
	f := ParseFailedUnits("● certbot.service loaded failed failed Certbot\nfoo.timer loaded failed failed Foo\n")
	if !reflect.DeepEqual(f, []string{"certbot.service", "foo.timer"}) {
		t.Fatal(f)
	}
	if f := ParseFailedUnits(""); f == nil || len(f) != 0 {
		t.Fatal("no failed units is an empty list")
	}
	st := ParseIsActive([]string{"otc_bridge", "mysql", "ufw"}, "active\ninactive\n")
	if st["otc_bridge"] != "active" || st["mysql"] != "inactive" || st["ufw"] != "unknown" {
		t.Fatal(st)
	}
}

func TestParseRedisInfo(t *testing.T) {
	r := ParseRedisInfo("# Server\r\nredis_version:7.0.15\r\nuptime_in_seconds:3600\r\n# Memory\r\nused_memory:1048576\r\nmaxmemory:0\r\n# Clients\r\nconnected_clients:7\r\n# Stats\r\nevicted_keys:0\r\nrejected_connections:2\r\n# Keyspace\r\ndb0:keys=120,expires=118,avg_ttl=1000\r\ndb1:keys=3,expires=0,avg_ttl=0\r\n")
	if !r.Up || r.Version != "7.0.15" || r.Uptime != 3600 || r.Used != 1048576 || r.Clients != 7 || r.Keys != 123 || r.Rejected != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestGTIDBacklog(t *testing.T) {
	a, err := ParseGTIDSet("3E11FA47-71CA-11E1-9E33-C80AA9429562:1-100,\n0ad6eae9-2d66-11e6-864f-ecf4bbd8c8a5:1-5:7-9")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseGTIDSet("3e11fa47-71ca-11e1-9e33-c80aa9429562:1-90:95,0ad6eae9-2d66-11e6-864f-ecf4bbd8c8a5:1-9")
	if err != nil {
		t.Fatal(err)
	}
	// 91-94 and 96-100 are missing: 9.
	if n := GTIDMissing(a, b); n != 9 {
		t.Fatal(n)
	}
	if n := GTIDMissing(b, b); n != 0 {
		t.Fatal(n)
	}
	// Tagged GTIDs (MySQL 8.3+).
	c, err := ParseGTIDSet("3e11fa47-71ca-11e1-9e33-c80aa9429562:1-3:mytag:1-2")
	if err != nil || len(c) != 2 || GTIDMissing(c, GTIDSet{}) != 5 {
		t.Fatal(c, err)
	}
	if _, err := ParseGTIDSet("uuid:9-1"); err == nil {
		t.Fatal("a backwards interval must fail")
	}
	if s, err := ParseGTIDSet(""); err != nil || len(s) != 0 {
		t.Fatal(s, err)
	}
}

func TestReplicaFromRowMySQL8(t *testing.T) {
	r := ReplicaFromRow(map[string]string{
		"Replica_IO_Running":    "Yes",
		"Replica_SQL_Running":   "No",
		"Seconds_Behind_Source": "NULL",
		"Source_Host":           "10.10.0.2",
		"Last_SQL_Errno":        "1062",
		"Last_SQL_Error":        "Could not execute Write_rows event on table otc.accounts; Duplicate entry 'someone@example.com' for key 'accounts.email'",
		"Retrieved_Gtid_Set":    "u:1-10",
		"Executed_Gtid_Set":     "u:1-7",
	})
	if r.IORunning != "Yes" || r.SQLRunning != "No" || r.Behind != nil || r.SQLErrno != 1062 || r.Source != "10.10.0.2" || r.Backlog != 3 {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.SQLError, "someone@example.com") || !strings.Contains(r.SQLError, "Duplicate entry") {
		t.Fatalf("the quoted value must be taken out: %q", r.SQLError)
	}
}

func TestReplicaFromRowMariaDB(t *testing.T) {
	r := ReplicaFromRow(map[string]string{
		"Slave_IO_Running":      "Connecting",
		"Slave_SQL_Running":     "Yes",
		"Seconds_Behind_Master": "12",
		"Master_Host":           "10.10.0.2",
		"Last_IO_Errno":         "2003",
		"Last_IO_Error":         "error connecting to master 'repl@10.10.0.2:3306' - retry-time: 60",
	})
	if r.IORunning != "Connecting" || r.SQLRunning != "Yes" || r.Behind == nil || *r.Behind != 12 || r.IOErrno != 2003 || r.Backlog != -1 {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.IOError, "repl@") {
		t.Fatalf("quoted part kept: %q", r.IOError)
	}
}

func TestReadCertSkipsKeys(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	notAfter := time.Now().Add(40 * 24 * time.Hour).Truncate(time.Second)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "off-the.cloud"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	// A key before the certificate, as some bundles have it.
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})) +
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	p := filepath.Join(t.TempDir(), "fullchain.pem")
	if err := os.WriteFile(p, []byte(pemText), 0600); err != nil {
		t.Fatal(err)
	}
	c := ReadCert(p)
	if c.Err != "" || c.Subject != "off-the.cloud" || !c.NotAfter.Equal(notAfter.UTC()) || c.Name != "fullchain.pem" {
		t.Fatalf("%+v", c)
	}
	if c := ReadCert(filepath.Join(t.TempDir(), "missing.pem")); c.Err == "" {
		t.Fatal("a missing file must say so")
	}
}
