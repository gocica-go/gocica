//go:build dev

package metrics

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

var (
	startTime    = time.Now()
	gaugesLocker = &sync.RWMutex{}
	gauges       = []*Gauge{}
)

func NewGauge(name string) *Gauge {
	gauge := &Gauge{
		name: name,
	}

	gaugesLocker.Lock()
	defer gaugesLocker.Unlock()

	gauges = append(gauges, gauge)

	return gauge
}

func WriteMetrics(w io.Writer) error {
	csvWriter := csv.NewWriter(w)
	defer csvWriter.Flush()

	csvWriter.Write([]string{"name", "value", "time"})

	gaugesLocker.RLock()
	defer gaugesLocker.RUnlock()

	for _, gauge := range gauges {
		for _, record := range gauge.getRecords() {
			err := csvWriter.Write([]string{
				gauge.name,
				strconv.FormatFloat(record.value, 'f', -1, 64),
				strconv.FormatFloat(record.time.Sub(startTime).Nanoseconds(), 'f', -1, 64),
			})
			if err != nil {
				return fmt.Errorf("write record: %w", err)
			}
		}
	}

	return nil
}

type record struct {
	value float64
	time  time.Time
	label string
}

type Gauge struct {
	name          string
	recordsLocker sync.RWMutex
	records       []record
}

func (g *Gauge) Set(value float64, label string) {
	g.recordsLocker.Lock()
	defer g.recordsLocker.Unlock()

	g.records = append(g.records, record{
		value: value,
		time:  time.Now(),
		label: label,
	})
}

func (g *Gauge) getRecords() []record {
	g.recordsLocker.RLock()
	defer g.recordsLocker.RUnlock()

	return g.records
}

func (g *Gauge) Stapwatch(f func(), label string) {
	start := time.Now()
	start = start.Round(0) // delete monotonic clock value
	f()
	g.Set(float64(time.Since(start).Nanoseconds()), label)
}
