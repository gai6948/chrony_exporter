// Copyright 2022 Ben Kochie
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package collector

import (
	"fmt"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/facebook/time/ntp/chrony"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	namespace = "chrony"
)

var (
	collectTracking    = kingpin.Flag("collector.tracking", "Collect tracking metrics").Default("true").Bool()
	collectSources     = kingpin.Flag("collector.sources", "Collect sources metrics").Default("false").Bool()
	collectChmodSocket = kingpin.Flag("collector.chmod-socket", "Chmod 0666 the receiving unix datagram socket").Default("false").Bool()
	collectDNSLookups  = kingpin.Flag("collector.dns-lookups", "do reverse DNS lookups").Default("true").Bool()

	upMetric = typedDesc{
		prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "up"),
			"Whether the chrony server is up.",
			nil,
			nil,
		),
		prometheus.GaugeValue,
	}
)

// Exporter collects chrony stats from the given server and exports
// them using the prometheus metrics package.
type Exporter struct {
	address string
	timeout time.Duration

	collectSources     bool
	collectTracking    bool
	collectChmodSocket bool
	collectDNSLookups  bool

	logger log.Logger
}

type typedDesc struct {
	desc      *prometheus.Desc
	valueType prometheus.ValueType
}

func (d *typedDesc) mustNewConstMetric(value float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d.desc, d.valueType, value, labels...)
}

func NewExporter(address string, logger log.Logger) Exporter {

	return Exporter{
		address: address,
		timeout: 5 * time.Second,

		collectSources:     *collectSources,
		collectTracking:    *collectTracking,
		collectChmodSocket: *collectChmodSocket,
		collectDNSLookups:  *collectDNSLookups,

		logger: logger,
	}
}

// Describe implements prometheus.Collector.
func (e Exporter) Describe(ch chan<- *prometheus.Desc) {
}

func (e Exporter) dial() (net.Conn, error, func()) {
	if strings.HasPrefix(e.address, "unix://") {
		remote := strings.TrimPrefix(e.address, "unix://")
		base, _ := path.Split(remote)
		local := path.Join(base, fmt.Sprintf("chrony_exporter.%d.sock", os.Getpid()))
		conn, err := net.DialUnix("unixgram",
			&net.UnixAddr{Name: local, Net: "unixgram"},
			&net.UnixAddr{Name: remote, Net: "unixgram"},
		)
		if err != nil {
			return nil, err, func() { os.Remove(local) }
		}
		if e.collectChmodSocket {
			if err := os.Chmod(local, 0666); err != nil {
				return nil, err, func() { conn.Close(); os.Remove(local) }
			}
		}
		err = conn.SetReadDeadline(time.Now().Add(e.timeout))
		if err != nil {
			level.Debug(e.logger).Log("msg", "Couldn't set read-timeout for unix datagram socket", "err", err)
		}
		return conn, nil, func() { conn.Close(); os.Remove(local) }
	}

	conn, err := net.DialTimeout("udp", e.address, e.timeout)
	return conn, err, func() {}
}

// Collect implements prometheus.Collector.
func (e Exporter) Collect(ch chan<- prometheus.Metric) {
	var up float64
	defer func() {
		ch <- upMetric.mustNewConstMetric(up)
	}()
	conn, err, cleanup := e.dial()
	defer cleanup()
	if err != nil {
		level.Debug(e.logger).Log("msg", "Couldn't connect to chrony", "address", e.address, "err", err)
		return
	}

	up = 1

	client := chrony.Client{Sequence: 1, Connection: conn}

	if e.collectSources {
		err = e.getSourcesMetrics(ch, client)
		if err != nil {
			level.Debug(e.logger).Log("msg", "Couldn't get sources", "err", err)
			up = 0
		}
	}

	if e.collectTracking {
		err = e.getTrackingMetrics(ch, client)
		if err != nil {
			level.Debug(e.logger).Log("msg", "Couldn't get tracking", "err", err)
			up = 0
		}
	}
}
