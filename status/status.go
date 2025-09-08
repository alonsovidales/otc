package status

import (
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"golang.org/x/sys/unix"
	"net"
	"net/http"
	"time"
)

func diskUsage(path string) (all, used, free uint64, err error) {
	var stat unix.Statfs_t
	if err = unix.Statfs(path, &stat); err != nil {
		return
	}

	// block size * number of blocks
	all = stat.Blocks * uint64(stat.Bsize)
	free = stat.Bfree * uint64(stat.Bsize)
	used = all - free

	return
}

func GetStatus(r *http.Request) (st *pb.Status, err error) {
	sizeDisk, usedDisk, _, err := diskUsage("/")
	if err != nil {
		log.Error("error reading disk stats:", err)
	}
	sizeRaid, usedRaid, _, err := diskUsage(cfg.GetStr("otc", "storage-path"))
	if err != nil {
		log.Error("error reading RAID stats:", err)
	}
	cpuPerc, err := cpu.Percent(time.Second, false)
	if err != nil {
		log.Error("error reading CPU stats:", err)
	}
	vmStat, err := mem.VirtualMemory()
	if err != nil {
		log.Error("error reading memory usage:", err)
	}

	localAddr := r.Context().Value(http.LocalAddrContextKey).(net.Addr)

	st = &pb.Status{
		Online:  true,
		LocalIp: localAddr.String(),
		//Errors:        []*pb.StatusErrors,
		RaidSize:    int32(sizeRaid / 1024 / 1000),
		RaidUsage:   int32(usedRaid / 1024 / 1000),
		DiskSize:    int32(sizeDisk / 1024 / 1000),
		DiskUsage:   int32(usedDisk / 1024 / 1000),
		CpuUsagePrc: float32(cpuPerc[0]),
		MemSize:     int32(vmStat.Total / 1024 / 1000),
		MemUsage:    int32(vmStat.Used / 1024 / 1000),
	}

	return
}
