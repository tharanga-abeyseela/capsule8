// Copyright 2017 Capsule8, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"
	"runtime"

	"github.com/capsule8/capsule8/pkg/sys/perf"
	"github.com/golang/glog"
)

const (
	// How many cache loads to sample on. After each sample period
	// of this many cache loads, the cache miss rate is calculated
	// and examined. This value tunes the trade-off between CPU
	// load and detection accuracy.
	LLCLoadSampleSize = 10000

	// Alarm thresholds as cache miss rates (between 0 and 1).
	// These values tune the trade-off between false negatives and
	// false positives.
	alarmThresholdInfo    = 0.95
	alarmThresholdWarning = 0.98
	alarmThresholdError   = 0.99

	// perf_event_attr config value for LL cache loads
	perfConfigLLCLoads = perf.PERF_COUNT_HW_CACHE_LL |
		(perf.PERF_COUNT_HW_CACHE_OP_READ << 8) |
		(perf.PERF_COUNT_HW_CACHE_RESULT_ACCESS << 16)

	// perf_event_attr config value for LL cache misses
	perfConfigLLCLoadMisses = perf.PERF_COUNT_HW_CACHE_LL |
		(perf.PERF_COUNT_HW_CACHE_OP_READ << 8) |
		(perf.PERF_COUNT_HW_CACHE_RESULT_MISS << 16)
)

type eventCounters struct {
	LLCLoads      uint64
	LLCLoadMisses uint64
}

var (
	cpuCounters []eventCounters
	loadsID     uint64
	missesID    uint64
)

func main() {
	flag.Set("logtostderr", "true")
	flag.Parse()

	glog.Infof("Starting Capsule8 cache side channel detector")

	//
	// Create our event monitor to read LL cache accesses and misses
	//
	// We ask the kernel to sample every LLCLoadSampleSize LLC
	// loads. During each sample, the LLC load misses are also
	// recorded, as well as CPU number, PID/TID, and sample time.
	//
	monitor, err := perf.NewEventMonitor()
	if err != nil {
		glog.Fatalf("Could not create EventMonitor: %s", err)
	}

	loadsAttr := &perf.EventAttr{
		SamplePeriod: LLCLoadSampleSize,
		SampleType:   perf.PERF_SAMPLE_CPU | perf.PERF_SAMPLE_TID | perf.PERF_SAMPLE_READ,
		Disabled:     true,
		Pinned:       true,
		Exclusive:    true,
		WakeupEvents: 1,
	}
	loadsID, err = monitor.RegisterHardwareCacheEvent(perfConfigLLCLoads,
		perf.WithEventAttr(loadsAttr))
	if err != nil {
		glog.Fatalf("Could not register event for LLC loads: %s", err)
	}

	missesAttr := &perf.EventAttr{
		Disabled: true,
	}
	missesID, err = monitor.RegisterHardwareCacheEvent(perfConfigLLCLoadMisses,
		perf.WithEventAttr(missesAttr))
	if err != nil {
		glog.Fatalf("Could not register event for LLC misses: %s", err)
	}

	// Allocate counters per CPU
	cpuCounters = make([]eventCounters, runtime.NumCPU())

	glog.Info("Monitoring for cache side channels")
	monitor.Run(onSample)
}

func onSample(eventID uint64, sample interface{}, err error) {
	var (
		counters eventCounters
		sr       *perf.SampleRecord
	)

	// The sample record contains all values in the event group,
	// tagged with their event ID
	if eventID == loadsID {
		sr = sample.(*perf.SampleRecord)
		for _, v := range sr.V.Values {
			counters.LLCLoads = v.Value
		}
	} else if eventID == missesID {
		sr = sample.(*perf.SampleRecord)
		for _, v := range sr.V.Values {
			counters.LLCLoadMisses = v.Value
		}
	} else {
		return
	}

	cpu := sr.CPU
	prevCounters := cpuCounters[cpu]
	cpuCounters[cpu] = counters

	counterDeltas := eventCounters{
		LLCLoads:      counters.LLCLoads - prevCounters.LLCLoads,
		LLCLoadMisses: counters.LLCLoadMisses - prevCounters.LLCLoadMisses,
	}

	alarm(sr, counterDeltas)
}

func alarm(sr *perf.SampleRecord, counters eventCounters) {
	LLCLoadMissRate := float32(counters.LLCLoadMisses) / float32(counters.LLCLoads)

	if LLCLoadMissRate > alarmThresholdError {
		glog.Errorf("cpu=%v pid=%v tid=%v LLCLoadMissRate=%v",
			sr.CPU, sr.Pid, sr.Tid, LLCLoadMissRate)
	} else if LLCLoadMissRate > alarmThresholdWarning {
		glog.Warningf("cpu=%v pid=%v tid=%v LLCLoadMissRate=%v",
			sr.CPU, sr.Pid, sr.Tid, LLCLoadMissRate)
	} else if LLCLoadMissRate > alarmThresholdInfo {
		glog.Infof("cpu=%v pid=%v tid=%v LLCLoadMissRate=%v",
			sr.CPU, sr.Pid, sr.Tid, LLCLoadMissRate)
	}
}
