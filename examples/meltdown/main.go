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
	"errors"
	"flag"

	"github.com/capsule8/capsule8/pkg/sys/perf"
	"github.com/golang/glog"
)

type userPageFault struct {
	pid       int32
	ip        uint64
	address   uint64
	errorCode uint64
}

var pageFaultsByPid map[int32]uint

const (
	alarmThresholdInfo     = 1
	alarmThresholdLow      = 10
	alarmThresholdMed      = 100
	alarmThresholdHigh     = 1000
	alarmThresholdCritical = 10000
)

func main() {
	flag.Set("alsologtostderr", "true")
	flag.Parse()

	glog.Info("Starting Capsule8 Meltdown Detector")

	monitor, err := perf.NewEventMonitor()
	if err != nil {
		glog.Fatal(err)
	}

	//
	// Look for segmentation faults trying to read kernel memory addresses
	//
	filter := "address > 0xffff000000000000"
	_, err = monitor.RegisterTracepoint("exceptions/page_fault_user",
		onPageFaultUser, perf.WithFilter(filter))

	if err != nil {
		glog.Fatal(err)
	}

	pageFaultsByPid = make(map[int32]uint)

	glog.Info("Monitoring for meltdown exploitation attempts")
	monitor.Run(onSample)
}

func onSample(eventID uint64, sample perf.EventMonitorSample) {
	if sample.Err != nil {
		glog.Fatal(sample.Err)
	}

	upf := sample.DecodedSample.(userPageFault)

	glog.V(10).Infof("%+v", sample.DecodedSample)

	pageFaultsByPid[upf.pid]++

	faults := pageFaultsByPid[upf.pid]
	if faults == alarmThresholdInfo {
		glog.Infof("pid %d kernel address page faults = %d",
			upf.pid, faults)
	} else if faults == alarmThresholdLow {
		glog.Warningf("pid %d kernel address page faults = %d",
			upf.pid, faults)
	} else if faults == alarmThresholdMed {
		glog.Warningf("pid %d kernel address page faults = %d",
			upf.pid, faults)
	} else if faults == alarmThresholdHigh {
		glog.Errorf("pid %d kernel address page faults = %d",
			upf.pid, faults)
	} else if faults == alarmThresholdCritical {
		glog.Errorf("pid %d kernel address page faults = %d",
			upf.pid, faults)
	}
}

func onPageFaultUser(
	sample *perf.SampleRecord,
	data perf.TraceEventSampleData,
) (interface{}, error) {
	pid, ok := data["common_pid"].(int32)
	if !ok {
		return nil, errors.New("common_pid not found")
	}

	address, ok := data["address"].(uint64)
	if !ok {
		return nil, errors.New("address not found")
	}

	ip, ok := data["ip"].(uint64)
	if !ok {
		return nil, errors.New("ip not found")
	}

	errorCode, ok := data["error_code"].(uint64)
	if !ok {
		return nil, errors.New("error_code not found")
	}

	upf := userPageFault{
		pid:       pid,
		ip:        ip,
		address:   address,
		errorCode: errorCode,
	}

	return upf, nil
}
