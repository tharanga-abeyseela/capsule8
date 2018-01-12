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

package perf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// TraceEventSampleData is a type alias for map[string]interface{}, which is
// the representation of sample data parsed from a Linux kernel sample.
type TraceEventSampleData map[string]interface{}

// TraceEventDecoderFn is the signature of a function to call to decode a
// sample. The first argument is the sample to be decoded, and the second
// is the parsed sample data.
type TraceEventDecoderFn func(*SampleRecord, TraceEventSampleData) (interface{}, error)

type traceEventDecoder struct {
	fields    map[string]traceEventField
	decoderfn TraceEventDecoderFn
}

func newTraceEventDecoder(tracingDir, name string, fn TraceEventDecoderFn) (*traceEventDecoder, uint16, error) {
	id, fields, err := getTraceEventFormat(tracingDir, name)
	if err != nil {
		return nil, 0, err
	}

	decoder := &traceEventDecoder{
		fields:    fields,
		decoderfn: fn,
	}

	return decoder, id, err
}

func decodeDataType(dataType int32, rawData []byte) (interface{}, error) {
	switch dataType {
	case TraceEventFieldTypeString:
		return nil, errors.New("internal error; got unexpected TraceEventFieldTypeString")
	case TraceEventFieldTypeSignedInt8:
		return int8(rawData[0]), nil
	case TraceEventFieldTypeSignedInt16:
		return int16(binary.LittleEndian.Uint16(rawData)), nil
	case TraceEventFieldTypeSignedInt32:
		return int32(binary.LittleEndian.Uint32(rawData)), nil
	case TraceEventFieldTypeSignedInt64:
		return int64(binary.LittleEndian.Uint64(rawData)), nil
	case TraceEventFieldTypeUnsignedInt8:
		return uint8(rawData[0]), nil
	case TraceEventFieldTypeUnsignedInt16:
		return binary.LittleEndian.Uint16(rawData), nil
	case TraceEventFieldTypeUnsignedInt32:
		return binary.LittleEndian.Uint32(rawData), nil
	case TraceEventFieldTypeUnsignedInt64:
		return binary.LittleEndian.Uint64(rawData), nil
	}
	return nil, errors.New("internal error; undefined dataType")
}

func (d *traceEventDecoder) decodeRawData(rawData []byte) (TraceEventSampleData, error) {
	data := make(TraceEventSampleData)
	for _, field := range d.fields {
		var arraySize, dataLength, dataOffset int
		var err error

		if field.dataLocSize > 0 {
			switch field.dataLocSize {
			case 4:
				dataOffset = int(binary.LittleEndian.Uint16(rawData[field.Offset:]))
				dataLength = int(binary.LittleEndian.Uint16(rawData[field.Offset+2:]))
			case 8:
				dataOffset = int(binary.LittleEndian.Uint32(rawData[field.Offset:]))
				dataLength = int(binary.LittleEndian.Uint32(rawData[field.Offset+4:]))
			default:
				return nil, fmt.Errorf("__data_loc size is neither 4 nor 8 (got %d)", field.dataLocSize)
			}

			if field.dataType == TraceEventFieldTypeString {
				if dataLength > 0 && rawData[dataOffset+dataLength-1] == 0 {
					dataLength--
				}
				data[field.FieldName] = string(rawData[dataOffset : dataOffset+dataLength])
				continue
			}
			arraySize = dataLength / field.dataTypeSize
		} else if field.arraySize == 0 {
			data[field.FieldName], err = decodeDataType(field.dataType, rawData[field.Offset:])
			if err != nil {
				return nil, err
			}
			continue
		} else {
			arraySize = field.arraySize
			dataOffset = field.Offset
			dataLength = arraySize * field.dataTypeSize
		}

		var array = make([]interface{}, arraySize)
		for i := 0; i < arraySize; i++ {
			array[i], err = decodeDataType(field.dataType, rawData[dataOffset:])
			if err != nil {
				return nil, err
			}
			dataOffset += field.dataTypeSize
		}
		data[field.FieldName] = array
	}

	return data, nil
}

type decoderMap struct {
	decoders map[uint16]*traceEventDecoder
	names    map[string]uint16
}

func newDecoderMap() *decoderMap {
	return &decoderMap{
		decoders: make(map[uint16]*traceEventDecoder),
		names:    make(map[string]uint16),
	}
}

func (dm *decoderMap) add(name string, id uint16, decoder *traceEventDecoder) {
	dm.decoders[id] = decoder
	dm.names[name] = id
}

type traceEventDecoderMap struct {
	sync.Mutex              // used only by writers
	active     atomic.Value // *decoderMap
	tracingDir string
}

func (m *traceEventDecoderMap) getDecoderMap() *decoderMap {
	value := m.active.Load()
	if value == nil {
		return nil
	}
	return value.(*decoderMap)
}

func newTraceEventDecoderMap(tracingDir string) *traceEventDecoderMap {
	return &traceEventDecoderMap{
		tracingDir: tracingDir,
	}
}

// Add a decoder "in-place". i.e., don't copy the decoder map before update
// No synchronization is used. Assumes the caller has adequate protection
func (m *traceEventDecoderMap) addDecoderInPlace(name string, fn TraceEventDecoderFn) (uint16, error) {
	decoder, id, err := newTraceEventDecoder(m.tracingDir, name, fn)
	if err != nil {
		return 0, err
	}

	dm := m.getDecoderMap()
	if dm == nil {
		dm = newDecoderMap()
		m.active.Store(dm)
	}
	dm.add(name, id, decoder)

	return id, nil
}

// Add a decoder safely. Proper synchronization is used to prevent multiple
// writers from stomping on each other while allowing readers to always
// operate without locking
func (m *traceEventDecoderMap) AddDecoder(name string, fn TraceEventDecoderFn) (uint16, error) {
	decoder, id, err := newTraceEventDecoder(m.tracingDir, name, fn)
	if err != nil {
		return 0, err
	}

	m.Lock()
	defer m.Unlock()

	odm := m.getDecoderMap()
	ndm := newDecoderMap()
	if odm != nil {
		for k, v := range odm.decoders {
			ndm.decoders[k] = v
		}
		for k, v := range odm.names {
			ndm.names[k] = v
		}
	}
	ndm.add(name, id, decoder)

	m.active.Store(ndm)

	return id, nil
}

// Remove a decoder "in-place". i.e., don't copy the decoder map before update
// No synchronization is used. Assumes the caller has adequate protection
func (m *traceEventDecoderMap) removeDecoderInPlace(name string) {
	dm := m.getDecoderMap()
	if dm == nil {
		return
	}

	id, ok := dm.names[name]
	if ok {
		delete(dm.names, name)
		delete(dm.decoders, id)
	}
}

// Remove a decoder safely. Proper synchronization is used to prevent multiple
// writers from stomping on each other while allowing readers to always
// operate without locking
func (m *traceEventDecoderMap) RemoveDecoder(name string) {
	dm := m.getDecoderMap()
	if dm == nil {
		return
	}

	id, ok := dm.names[name]
	if ok {
		m.Lock()
		defer m.Unlock()

		odm := m.getDecoderMap()
		if odm != nil {
			ndm := newDecoderMap()
			for k, v := range odm.decoders {
				if k != id {
					ndm.decoders[k] = v
				}
			}
			for k, v := range odm.names {
				if k != name {
					ndm.names[k] = v
				}
			}

			m.active.Store(ndm)
		}
	}
}

func (m *traceEventDecoderMap) getDecoder(eventType uint16) *traceEventDecoder {
	dm := m.getDecoderMap()
	if dm == nil {
		return nil
	}
	return dm.decoders[eventType]
}

func (m *traceEventDecoderMap) DecodeSample(sample *SampleRecord) (TraceEventSampleData, interface{}, error) {
	eventType := uint16(binary.LittleEndian.Uint64(sample.RawData))
	decoder := m.getDecoder(eventType)
	if decoder == nil {
		// Not an error. There just isn't a decoder for this sample
		return nil, nil, nil
	}

	data, err := decoder.decodeRawData(sample.RawData)
	if err != nil {
		return nil, nil, err
	}

	decodedSample, err := decoder.decoderfn(sample, data)
	return data, decodedSample, err
}
