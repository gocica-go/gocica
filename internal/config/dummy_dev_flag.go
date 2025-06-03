//go:build !dev

package config

type DevFlag struct{}

func (d DevFlag) StartProfiling() error {
	return nil
}

func (d DevFlag) StopProfiling() {}
