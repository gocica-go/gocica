//go:build dev

package main

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/pprof"
)

type DevFlag struct {
	CPUProf     string `kong:"optional,help='CPU profile output file',type='path'"`
	CPUProfRate int    `kong:"optional,help='CPU profiling rate in Hz',default='100'"`
	CPUProfFile *os.File
	MemProf     string `kong:"optional,help='Memory profile output file',type='path'"`
}

func (d DevFlag) StartProfiling() error {
	if d.CPUProf != "" {
		var err error
		d.CPUProfFile, err = os.Create(d.CPUProf)
		if err != nil {
			return fmt.Errorf("failed to create CPU profile file: %w", err)
		}

		runtime.SetCPUProfileRate(d.CPUProfRate)

		if err := pprof.StartCPUProfile(d.CPUProfFile); err != nil {
			return fmt.Errorf("failed to start CPU profiling: %w", err)
		}
	}

	return nil
}

func (d DevFlag) StopProfiling() {
	if d.CPUProfFile != nil {
		pprof.StopCPUProfile()
		d.CPUProfFile.Close()
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
}
