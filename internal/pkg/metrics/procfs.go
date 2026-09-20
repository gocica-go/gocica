package metrics

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/prometheus/procfs"
	"github.com/prometheus/procfs/blockdevice"
)

var (
	cpuAllGauge    = NewGauge("cpu_all")
	cpuSelfGauge   = NewGauge("cpu_self")
	memAllGauge    = NewGauge("mem_all")
	memSelfGauge   = NewGauge("mem_self")
	networkRxGauge = NewGauge("network_rx")
	networkTxGauge = NewGauge("network_tx")
	// Cumulative, per block device: bytes written, write requests completed,
	// and milliseconds the device spent with I/O in flight. Rates come from
	// differencing consecutive samples.
	diskWriteBytesGauge = NewGauge("disk_write_bytes")
	diskWriteIOsGauge   = NewGauge("disk_write_ios")
	diskIOTimeGauge     = NewGauge("disk_io_time")
)

func InitProcStat() error {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return fmt.Errorf("create procfs: %w", err)
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	go func() {
		for range ticker.C {
			err := getCPUAllStat(fs)
			if err != nil {
				log.Printf("failed to get stat: %v", err)
			}

			err = getMemAllStat(fs)
			if err != nil {
				log.Printf("failed to get stat: %v", err)
			}

			err = getSelfStat(fs)
			if err != nil {
				log.Printf("failed to get stat: %v", err)
			}

			err = getDiskStat(fs)
			if err != nil {
				log.Printf("failed to get stat: %v", err)
			}
		}
	}()

	return nil
}

func getCPUAllStat(fs procfs.FS) error {
	stat, err := fs.Stat()
	if err != nil {
		return fmt.Errorf("get stat: %w", err)
	}

	cpuAllGauge.Set(float64(stat.CPUTotal.User), "user")
	cpuAllGauge.Set(float64(stat.CPUTotal.System), "system")
	cpuAllGauge.Set(float64(stat.CPUTotal.Idle), "idle")
	cpuAllGauge.Set(float64(stat.CPUTotal.Iowait), "iowait")
	cpuAllGauge.Set(float64(stat.CPUTotal.Nice), "nice")
	cpuAllGauge.Set(float64(stat.CPUTotal.IRQ), "irq")
	cpuAllGauge.Set(float64(stat.CPUTotal.SoftIRQ), "softirq")
	cpuAllGauge.Set(float64(stat.CPUTotal.Steal), "steal")

	return nil
}

func getMemAllStat(fs procfs.FS) error {
	mem, err := fs.Meminfo()
	if err != nil {
		return fmt.Errorf("get stat: %w", err)
	}

	if mem.MemTotal != nil {
		memAllGauge.Set(float64(*mem.MemTotal), "total")
	}
	if mem.Buffers != nil {
		memAllGauge.Set(float64(*mem.Buffers), "buffers")
	}
	if mem.Cached != nil {
		memAllGauge.Set(float64(*mem.Cached), "cached")
	}
	if mem.Slab != nil {
		memAllGauge.Set(float64(*mem.Slab), "slab")
	}
	if mem.MemFree != nil {
		memAllGauge.Set(float64(*mem.MemFree), "free")
	}
	if mem.SwapTotal != nil {
		memAllGauge.Set(float64(*mem.SwapTotal), "swap_total")
	}
	if mem.SwapCached != nil {
		memAllGauge.Set(float64(*mem.SwapCached), "swap_cached")
	}
	if mem.SwapFree != nil {
		memAllGauge.Set(float64(*mem.SwapFree), "swap_free")
	}

	return nil
}

func getSelfStat(fs procfs.FS) error {
	proc, err := fs.Self()
	if err != nil {
		return fmt.Errorf("get stat: %w", err)
	}

	stat, err := proc.Stat()
	if err != nil {
		return fmt.Errorf("get stat: %w", err)
	}

	cpuSelfGauge.Set(float64(stat.CPUTime()), "total")
	memSelfGauge.Set(float64(stat.ResidentMemory()), "resident")
	memSelfGauge.Set(float64(stat.VirtualMemory()), "virtual")

	netDev, err := proc.NetDev()
	if err != nil {
		return fmt.Errorf("get stat: %w", err)
	}

	for _, dev := range netDev {
		networkRxGauge.Set(float64(dev.RxBytes), dev.Name)
		networkTxGauge.Set(float64(dev.TxBytes), dev.Name)
	}

	return nil
}

// sectorSize is what /proc/diskstats counts in, regardless of the device's
// actual sector size.
const sectorSize = 512

func getDiskStat(procfs.FS) error {
	bfs, err := blockdevice.NewDefaultFS()
	if err != nil {
		return fmt.Errorf("create blockdevice fs: %w", err)
	}

	stats, err := bfs.ProcDiskstats()
	if err != nil {
		return fmt.Errorf("get diskstats: %w", err)
	}

	for _, d := range stats {
		// Whole disks only: partitions and loop devices would double count.
		if strings.HasPrefix(d.DeviceName, "loop") || strings.HasPrefix(d.DeviceName, "dm-") ||
			strings.HasPrefix(d.DeviceName, "sr") || isPartition(d.DeviceName) {
			continue
		}

		diskWriteBytesGauge.Set(float64(d.WriteSectors)*sectorSize, d.DeviceName)
		diskWriteIOsGauge.Set(float64(d.WriteIOs), d.DeviceName)
		diskIOTimeGauge.Set(float64(d.IOsTotalTicks), d.DeviceName)
	}

	return nil
}

// isPartition recognises "sda1" and "nvme0n1p1", but not "sda" or "nvme0n1".
func isPartition(name string) bool {
	if strings.HasPrefix(name, "nvme") {
		return strings.Contains(name, "p")
	}

	return len(name) > 0 && name[len(name)-1] >= '0' && name[len(name)-1] <= '9'
}
