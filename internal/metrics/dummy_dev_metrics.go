//go:build !dev

package metrics

import "io"

func NewGauge(string) *Gauge {
	return nil
}

func WriteMetrics(io.Writer) error {
	return nil
}

type Gauge struct{}

func (g *Gauge) Set(float64, string) {}

func (g *Gauge) Stapwatch(f func(), lebel string) {
	f()
}
