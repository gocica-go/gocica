package metrics

import (
	"fmt"
	"log"
	"time"

	"github.com/prometheus/procfs"
)

var (
	cpuAllGauge    = NewGauge("cpu_all")
	cpuSelfGauge   = NewGauge("cpu_self")
	memAllGauge    = NewGauge("mem_all")
	memSelfGauge   = NewGauge("mem_self")
	networkRxGauge = NewGauge("network_rx")
	networkTxGauge = NewGauge("network_tx")
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
	cpuAllGauge.Set(float64(stat.CPUTotal.Guest), "guest")
	cpuAllGauge.Set(float64(stat.CPUTotal.GuestNice), "guest_nice")

	for id, cpu := range stat.CPU {
		cpuAllGauge.Set(float64(cpu.User), fmt.Sprintf("user%d", id))
		cpuAllGauge.Set(float64(cpu.System), fmt.Sprintf("system%d", id))
		cpuAllGauge.Set(float64(cpu.Idle), fmt.Sprintf("idle%d", id))
		cpuAllGauge.Set(float64(cpu.Iowait), fmt.Sprintf("iowait%d", id))
		cpuAllGauge.Set(float64(cpu.Nice), fmt.Sprintf("nice%d", id))
		cpuAllGauge.Set(float64(cpu.IRQ), fmt.Sprintf("irq%d", id))
		cpuAllGauge.Set(float64(cpu.SoftIRQ), fmt.Sprintf("softirq%d", id))
		cpuAllGauge.Set(float64(cpu.Steal), fmt.Sprintf("steal%d", id))
		cpuAllGauge.Set(float64(cpu.Guest), fmt.Sprintf("guest%d", id))
		cpuAllGauge.Set(float64(cpu.GuestNice), fmt.Sprintf("guest_nice%d", id))
	}

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
