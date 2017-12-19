// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// Package metrics provides general system and process level metrics collection.
package swarm

import (
	"net"
	"time"
	"fmt"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/metrics"
	vmetrics "github.com/rcrowley/go-metrics"
)

var gc vmetrics.GraphiteConfig
var (
	bmin = metrics.NewMeter("fab.DiscoveryBenchmarkMinDurations")
	bmax = metrics.NewMeter("fab.DiscoveryBenchmarkMaxDurations")
	bavg = metrics.NewMeter("fab.DiscoveryBenchmarkAverageDurations")
)

func SetupTestMetrics(namespace string) {
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:2003")

  fmt.Println(vmetrics.DefaultRegistry)
	gc = vmetrics.GraphiteConfig{
		Addr:          addr,
		Registry:      vmetrics.DefaultRegistry,
		FlushInterval: 500 * time.Millisecond,
		DurationUnit:  time.Nanosecond,
		Prefix:        "fab",
		Percentiles:   []float64{0.5, 0.75, 0.95, 0.99, 0.999},
	}

	go vmetrics.GraphiteWithConfig(gc)
}

func ShutdownTestMetrics() {
	vmetrics.GraphiteOnce(gc)
}

func TestMetrics(t *testing.T) {
  fmt.Println("Setting up metrics")
  SetupTestMetrics("test")
  fmt.Println("Done.")
  fmt.Println("Write some metrics...")
  testMetrics(t) 
  ShutdownTestMetrics()
}

func testMetrics(t *testing.T) {
  
  started := int64(time.Now().Nanosecond())
 
	var min, max , avg int64 

 for i:=1; i<1000;i++ {
    randn := int64(rand.Intn(50000) + 1000)
    max = randn
    min = started - randn
    avg = max / min 
    bmin.Mark(int64(min))
    bmax.Mark(int64(randn))
    bavg.Mark(int64(avg))
    time.Sleep(2*time.Second)
  }
  fmt.Println("Done.")
}
