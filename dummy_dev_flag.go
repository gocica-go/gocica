//go:build !dev

package main

type DevFlag struct{}

func (d DevFlag) StartProfiling() error {
	return nil
}

func (d DevFlag) StopProfiling() {}
