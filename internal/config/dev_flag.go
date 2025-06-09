//go:build dev

package config

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/pprof"

	"github.com/felixge/fgprof"
	"github.com/mazrean/gocica/internal/metrics"
)

type DevFlag struct {
	CPUProf     string       `kong:"optional,help='CPU profile output file',type='path'"`
	CPUProfFile *os.File     `kong:"-"`
	MemProf     string       `kong:"optional,help='Memory profile output file',type='path'"`
	Metrics     string       `kong:"optional,help='Metrics output file',type='path'"`
	MutexProf   string       `kong:"optional,help='Mutex profile output file',type='path'"`
	BlockProf   string       `kong:"optional,help='Block profile output file',type='path'"`
	FgProf      string       `kong:"optional,help='fgprof output file',type='path'"`
	fgprofStop  func() error `kong:"-"`
}

func (d *DevFlag) StartProfiling() error {
	if d.CPUProf != "" {
		f, err := os.Create(d.CPUProf)
		if err != nil {
			return fmt.Errorf("failed to create CPU profile file: %w", err)
		}
		d.CPUProfFile = f

		if err := pprof.StartCPUProfile(f); err != nil {
			return fmt.Errorf("failed to start CPU profiling: %w", err)
		}
	}

	if d.FgProf != "" {
		f, err := os.Create(d.FgProf)
		if err != nil {
			return fmt.Errorf("failed to create fgprof file: %w", err)
		}

		d.fgprofStop = fgprof.Start(f, fgprof.FormatPprof)
	}

	if d.BlockProf != "" {
		runtime.SetBlockProfileRate(1)
	}

	if d.MutexProf != "" {
		runtime.SetMutexProfileFraction(1)
	}

	if d.Metrics != "" {
		if err := metrics.InitProcStat(); err != nil {
			return fmt.Errorf("failed to initialize proc stat: %w", err)
		}
	}

	return nil
}

func (d *DevFlag) StopProfiling() {
	if d.CPUProfFile != nil {
		pprof.StopCPUProfile()
		defer d.CPUProfFile.Close()
	}

	if d.fgprofStop != nil {
		if err := d.fgprofStop(); err != nil {
			log.Printf("could not stop fgprof: %v", err)
		}
	}

	if d.MemProf != "" {
		f, err := os.Create(d.MemProf)
		if err != nil {
			log.Fatal("could not create memory profile: ", err)
		}
		defer f.Close()

		runtime.GC()

		if err := pprof.WriteHeapProfile(f); err != nil {
			log.Fatal("could not write memory profile: ", err)
		}
	}

	if d.MutexProf != "" {
		f, err := os.Create(d.MutexProf)
		if err != nil {
			log.Fatal("could not create mutex profile file: ", err)
		}
		defer f.Close()

		if err := pprof.Lookup("mutex").WriteTo(f, 0); err != nil {
			log.Fatal("could not write mutex profile: ", err)
		}
	}

	if d.BlockProf != "" {
		f, err := os.Create(d.BlockProf)
		if err != nil {
			log.Fatal("could not create block profile file: ", err)
		}
		defer f.Close()

		if err := pprof.Lookup("block").WriteTo(f, 0); err != nil {
			log.Fatal("could not write block profile: ", err)
		}
	}

	if d.Metrics != "" {
		f, err := os.Create(d.Metrics)
		if err != nil {
			log.Fatal("could not create metrics file: ", err)
		}
		defer f.Close()

		if err := metrics.WriteMetrics(f); err != nil {
			log.Fatal("could not write metrics: ", err)
		}
	}
}
