package agent

import (
	"context"
	"sync"
	"time"

	"bytes"
	"encoding/json"

	"github.com/evergreen-ci/evergreen/rest/client"
	system_metrics "github.com/evergreen-ci/timber/system_metrics"
	"github.com/mongodb/ftdc/metrics"
	"github.com/pkg/errors"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/host"
	"github.com/shirou/gopsutil/process"
)

type MetricCollector interface {
	Name() string
	Format() DataFormat
	//Format() interface{}
	Collect() ([]byte, error)
}

type SystemMetricCollector struct {
	mu          sync.Mutex
	wg          sync.WaitGroup
	innerCancel context.CancelFunc
	logger      client.LoggerProducer
	taskOpts    *system_metrics.SystemMetricsOptions
	connOpts    system_metrics.ConnectionOptions
	interval    time.Duration
	collectors  []MetricCollector
	id          string
	client      *system_metrics.SystemMetricsClient
	errored     bool
	closed      bool
}

type DiskUsageCollector struct{}

type UptimeCollector struct{}

type ProcessCollector struct{}

// DataFormat describes how the time series data is stored.
type DataFormat int32

// Valid DataFormat values.
const (
	DataFormatText DataFormat = 0
	DataFormatFTDC DataFormat = 1
	DataFormatBSON DataFormat = 2
	DataFormatJSON DataFormat = 3
	DataFormatCSV  DataFormat = 4
)

type UptimeWrapper struct {
	Uptime uint64 `json:"uptime"`
}

type ProcessesWrapper struct {
	Processes []ProcessWrapper
}

type ProcessWrapper struct {
	Pid           int32          `json:"pid"`
	CPUPercent    float64        `json:"%cpu"`
	MemoryPercent float64        `json:"%mem"`
	VSZ           uint64         `json:"vsz"`
	RSS           uint64         `json:"rss"`
	TT            string         `json:"tt"`
	Stat          string         `json:"stat"`
	Started       int64          `json:"started"`
	Time          *cpu.TimesStat `json:"time"`
	Command       string         `json:"command"`
}

func (collector *DiskUsageCollector) Name() string {
	return "disk_usage"
}

func (collector *UptimeCollector) Name() string {
	return "uptime"
}

func (collector *ProcessCollector) Name() string {
	return "process"
}

func (collector *DiskUsageCollector) Format() DataFormat {
	return DataFormatFTDC
}

func (collector *UptimeCollector) Format() DataFormat {
	return DataFormatFTDC
}

func (collector *ProcessCollector) Format() DataFormat {
	return DataFormatJSON
}

func (collector *DiskUsageCollector) Collect(ctx context.Context) ([]byte, error) {
	usage, err := disk.Usage("/")
	if err != nil {
		return nil, errors.Wrap(err, "problem capturing metrics with gopsutil")
	}

	return convertJSONToFTDC(ctx, usage)
}

func (collector *UptimeCollector) Collect(ctx context.Context) ([]byte, error) {
	uptime, err := host.Uptime()
	if err != nil {
		return nil, errors.Wrap(err, "problem capturing metrics with gopsutil")
	}

	uptimeWrapper := UptimeWrapper{uptime}
	return convertJSONToFTDC(ctx, uptimeWrapper)
}

func (collector *ProcessCollector) Collect(ctx context.Context) ([]byte, error) {
	processes, err := process.Processes()
	if err != nil {
		return nil, errors.Wrap(err, "problem capturing metrics with gopsutil")
	}

	//processesWithTimestamp := ProcessesWithTimestamp{time.Now(), metric}

	processesPopulated := make([]ProcessWrapper, len(processes))
	for i, process := range processes {
		cpuPercent, _ := process.CPUPercent()
		memoryPercent, _ := process.CPUPercent()
		memInfo, _ := process.MemoryInfo()
		terminal, _ := process.Terminal()
		status, _ := process.Status()
		createTime, _ := process.CreateTime()
		times, _ := process.Times()
		name, _ := process.Name()

		processWrapper := ProcessWrapper{
			Pid:           process.Pid,
			CPUPercent:    cpuPercent,
			MemoryPercent: memoryPercent,
			VSZ:           memInfo.VMS,
			RSS:           memInfo.RSS,
			TT:            terminal,
			Stat:          status,
			Started:       createTime,
			Time:          times,
			Command:       name,
		}
		processesPopulated[i] = processWrapper
	}
	processesWrapper := ProcessesWrapper{processesPopulated}
	return json.Marshal(processesWrapper)
	// return json.Marshal(processes)
}

func convertJSONToFTDC(ctx context.Context, metric interface{}) ([]byte, error) {
	jsonMetrics, err := json.Marshal(metric)
	//fmt.Println("\njsonMetrics:", string(jsonMetrics))
	if err != nil {
		return nil, errors.Wrap(err, "problem converting metrics to JSON")
	}

	opts := metrics.CollectJSONOptions{
		InputSource:   bytes.NewReader(jsonMetrics),
		SampleCount:   100,
		FlushInterval: 100 * time.Millisecond,
	}

	output, err := metrics.CollectJSONStream(ctx, opts)
	if err != nil {
		return nil, errors.Wrap(err, "problem converting FTDC to JSON")
	}
	return output, nil
}
