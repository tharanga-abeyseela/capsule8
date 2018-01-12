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
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/golang/glog"
)

const (
	_ int32 = iota

	// TraceEventFieldTypeString is a string.
	TraceEventFieldTypeString

	// TraceEventFieldTypeSignedInt8 is an 8-bit signed integer.
	TraceEventFieldTypeSignedInt8
	// TraceEventFieldTypeSignedInt16 is a 16-bit signed integer.
	TraceEventFieldTypeSignedInt16
	// TraceEventFieldTypeSignedInt32 is a 32-bit signed integer.
	TraceEventFieldTypeSignedInt32
	// TraceEventFieldTypeSignedInt64 is a 64-bit signed integer.
	TraceEventFieldTypeSignedInt64

	// TraceEventFieldTypeUnsignedInt8 is an 8-bit unsigned integer.
	TraceEventFieldTypeUnsignedInt8
	// TraceEventFieldTypeUnsignedInt16 is a 16-bit unsigned integer.
	TraceEventFieldTypeUnsignedInt16
	// TraceEventFieldTypeUnsignedInt32 is a 32-bit unsigned integer.
	TraceEventFieldTypeUnsignedInt32
	// TraceEventFieldTypeUnsignedInt64 is a 64-bit unsigned integer.
	TraceEventFieldTypeUnsignedInt64
)

type traceEventField struct {
	FieldName string
	TypeName  string
	Offset    int
	Size      int
	IsSigned  bool

	dataType     int32 // data type constant from above
	dataTypeSize int
	dataLocSize  int
	arraySize    int // 0 == [] array, >0 == # elements
}

func (field *traceEventField) setTypeFromSizeAndSign(isArray bool, arraySize int) (bool, error) {
	if isArray {
		if arraySize == -1 {
			// If this is an array of unknown size, we have to
			// skip it, because the field size is ambiguous
			return true, nil
		}
		field.dataTypeSize = field.Size / arraySize
	} else {
		field.dataTypeSize = field.Size
	}

	switch field.dataTypeSize {
	case 1:
		if field.IsSigned {
			field.dataType = TraceEventFieldTypeSignedInt8
		} else {
			field.dataType = TraceEventFieldTypeUnsignedInt8
		}
	case 2:
		if field.IsSigned {
			field.dataType = TraceEventFieldTypeSignedInt16
		} else {
			field.dataType = TraceEventFieldTypeUnsignedInt16
		}
	case 4:
		if field.IsSigned {
			field.dataType = TraceEventFieldTypeSignedInt32
		} else {
			field.dataType = TraceEventFieldTypeUnsignedInt32
		}
	case 8:
		if field.IsSigned {
			field.dataType = TraceEventFieldTypeSignedInt64
		} else {
			field.dataType = TraceEventFieldTypeUnsignedInt64
		}
	default:
		// We can't figure out the type from the information given to
		// us. We're here likely because of a typedef name we didn't
		// recognize that's an array of integers or something. Skip it.
		return true, nil
	}
	return false, nil
}

func (field *traceEventField) parseTypeName(s string, isArray bool, arraySize int) (bool, error) {
	if strings.HasPrefix(s, "const ") {
		s = s[6:]
	}

	switch s {
	// Standard C types
	case "bool":
		// "bool" is usually 1 byte, but it could be defined otherwise?
		return field.setTypeFromSizeAndSign(isArray, arraySize)

	// These types are going to be consistent in a 64-bit kernel, and in a
	// 32-bit kernel as well, except for "long".
	case "int", "signed int", "signed", "unsigned int", "unsigned", "uint":
		// The kernel is a bit unreliable about reporting "int" with
		// different sizes and signs, so try to use size/sign whenever
		// possible. If it's not possible, assume 32-bit int
		skip, err := field.setTypeFromSizeAndSign(isArray, arraySize)
		if skip && err == nil {
			if field.IsSigned {
				field.dataType = TraceEventFieldTypeSignedInt32
			} else {
				field.dataType = TraceEventFieldTypeUnsignedInt32
			}
			field.dataTypeSize = 4
			return false, nil
		}
		return skip, err
	case "char", "signed char", "unsigned char":
		if field.IsSigned {
			field.dataType = TraceEventFieldTypeSignedInt8
		} else {
			field.dataType = TraceEventFieldTypeUnsignedInt8
		}
		field.dataTypeSize = 1
		return false, nil
	case "short", "signed short", "unsigned short":
		if field.IsSigned {
			field.dataType = TraceEventFieldTypeSignedInt16
		} else {
			field.dataType = TraceEventFieldTypeUnsignedInt16
		}
		field.dataTypeSize = 2
		return false, nil
	case "long", "signed long", "unsigned long":
		skip, err := field.setTypeFromSizeAndSign(isArray, arraySize)
		if skip && err == nil {
			// Assume a 64-bit kernel
			if field.IsSigned {
				field.dataType = TraceEventFieldTypeSignedInt64
			} else {
				field.dataType = TraceEventFieldTypeUnsignedInt64
			}
			field.dataTypeSize = 8
			return false, nil
		}
		return skip, err
	case "long long", "signed long long", "unsigned long long":
		if field.IsSigned {
			field.dataType = TraceEventFieldTypeSignedInt64
		} else {
			field.dataType = TraceEventFieldTypeUnsignedInt64
		}
		field.dataTypeSize = 8
		return false, nil

	// Fixed-size types
	case "s8", "__s8", "int8_t", "__int8_t":
		field.dataType = TraceEventFieldTypeSignedInt8
		field.dataTypeSize = 1
		return false, nil
	case "u8", "__u8", "uint8_t", "__uint8_t":
		field.dataType = TraceEventFieldTypeSignedInt16
		field.dataTypeSize = 1
		return false, nil
	case "s16", "__s16", "int16_t", "__int16_t":
		field.dataType = TraceEventFieldTypeSignedInt16
		field.dataTypeSize = 2
		return false, nil
	case "u16", "__u16", "uint16_t", "__uint16_t":
		field.dataType = TraceEventFieldTypeUnsignedInt16
		field.dataTypeSize = 2
		return false, nil
	case "s32", "__s32", "int32_t", "__int32_t":
		field.dataType = TraceEventFieldTypeSignedInt32
		field.dataTypeSize = 4
		return false, nil
	case "u32", "__u32", "uint32_t", "__uint32_t":
		field.dataType = TraceEventFieldTypeUnsignedInt32
		field.dataTypeSize = 4
		return false, nil
	case "s64", "__s64", "int64_t", "__int64_t":
		field.dataType = TraceEventFieldTypeSignedInt64
		field.dataTypeSize = 8
		return false, nil
	case "u64", "__u64", "uint64_t", "__uint64_t":
		field.dataType = TraceEventFieldTypeUnsignedInt64
		field.dataTypeSize = 8
		return false, nil

		/*
			// Known kernel typedefs in 4.10
			case "clockid_t", "pid_t", "xfs_extnum_t":
				field.dataType = TraceEventFieldTypeSignedInt32
				field.dataTypeSize = 4
			case "dev_t", "gfp_t", "gid_t", "isolate_mode_t", "tid_t", "uid_t",
				"ext4_lblk_t",
				"xfs_agblock_t", "xfs_agino_t", "xfs_agnumber_t", "xfs_btnum_t",
				"xfs_dahash_t", "xfs_exntst_t", "xfs_extlen_t", "xfs_lookup_t",
				"xfs_nlink_t", "xlog_tid_t":
				field.dataType = TraceEventFieldTypeUnsignedInt32
				field.dataTypeSize = 4
			case "loff_t", "xfs_daddr_t", "xfs_fsize_t", "xfs_lsn_t", "xfs_off_t":
				field.dataType = TraceEventFieldTypeSignedInt64
				field.dataTypeSize = 8
			case "aio_context_t", "blkcnt_t", "cap_user_data_t",
				"cap_user_header_t", "cputime_t", "dma_addr_t", "fl_owner_t",
				"gfn_t", "gpa_t", "gva_t", "ino_t", "key_serial_t", "key_t",
				"mqd_t", "off_t", "pgdval_t", "phys_addr_t", "pmdval_t",
				"pteval_t", "pudval_t", "qid_t", "resource_size_t", "sector_t",
				"timer_t", "umode_t",
				"ext4_fsblk_t",
				"xfs_ino_t", "xfs_fileoff_t", "xfs_fsblock_t", "xfs_filblks_t":
				field.dataType = TraceEventFieldTypeUnsignedInt64
				field.dataTypeSize = 8

			case "xen_mc_callback_fn_t":
				// This is presumably a pointer type
				return true, nil

			case "uuid_be", "uuid_le":
				field.dataType = TraceEventFieldTypeUnsignedInt8
				field.dataTypeSize = 1
				field.arraySize = 16
				return false, nil
		*/

	default:
		// Judging by Linux kernel conventions, it would appear that
		// any type name ending in _t is an integer type. Try to figure
		// it out from other information the kernel has given us. Note
		// that pointer types also fall into this category; however, we
		// have no way to know whether the value is to be treated as an
		// integer or a pointer unless we try to parse the printf fmt
		// string that's also included in the format description (no!)
		if strings.HasSuffix(s, "_t") {
			return field.setTypeFromSizeAndSign(isArray, arraySize)
		}
		if len(s) > 0 && s[len(s)-1] == '*' {
			return field.setTypeFromSizeAndSign(isArray, arraySize)
		}
		if strings.HasPrefix(s, "struct ") {
			// Skip structs
			return true, nil
		}
		if strings.HasPrefix(s, "union ") {
			// Skip unions
			return true, nil
		}
		if strings.HasPrefix(s, "enum ") {
			return field.setTypeFromSizeAndSign(isArray, arraySize)
		}
		// We don't recognize the type name. It's probably a typedef
		// for an integer or array of integers or something. Try to
		// figure it out from the size and sign information, but the
		// odds are not in our favor if we're here.
		return field.setTypeFromSizeAndSign(isArray, arraySize)
	}
}

var linuxArraySizeSanityWarning = false

func (field *traceEventField) parseTypeAndName(s string) (bool, error) {
	if strings.HasPrefix(s, "__data_loc") {
		s = s[11:]
		field.dataLocSize = field.Size

		// We have to use the type name here. The size information will
		// always indicate how big the data_loc information is, which
		// is normally 4 bytes (offset uint16, length uint16)

		x := strings.LastIndexFunc(s, unicode.IsSpace)
		field.FieldName = string(strings.TrimSpace(s[x+1:]))

		s = s[:x]
		if !strings.HasSuffix(s, "[]") {
			return true, errors.New("Expected [] suffix on __data_loc type")
		}
		s = s[:len(s)-2]
		field.TypeName = string(s)

		if s == "char" {
			field.dataType = TraceEventFieldTypeString
			field.dataTypeSize = 1
		} else {
			skip, err := field.parseTypeName(s, true, -1)
			if err != nil {
				return true, err
			}
			if skip {
				return true, nil
			}
		}
		return false, nil
	}

	arraySize := -1
	isArray := false
	x := strings.IndexRune(s, '[')
	if x != -1 {
		if s[x+1] == ']' {
			return true, errors.New("Unexpected __data_loc without __data_loc prefix")
		}

		// Try to parse out the array size. Most of the time this will
		// be possible, but there are some cases where macros or consts
		// are used, so it's not possible.
		value, err := strconv.Atoi(s[x+1 : len(s)-1])
		if err == nil {
			arraySize = value
		}

		s = s[:x]
		isArray = true
	}

	x = strings.LastIndexFunc(s, unicode.IsSpace)
	field.TypeName = string(s[:x])
	field.FieldName = string(strings.TrimSpace(s[x+1:]))

	skip, err := field.parseTypeName(field.TypeName, isArray, arraySize)
	if err != nil {
		return true, err
	}
	if skip {
		return true, nil
	}
	if isArray {
		if arraySize >= 0 {
			field.arraySize = arraySize

			// Sanity check what we've determined. Various versions
			// of the Linux kernel misreport size information.
			if arraySize != field.Size/field.dataTypeSize {
				if !linuxArraySizeSanityWarning {
					linuxArraySizeSanityWarning = true
					glog.Warning("Linux kernel tracepoint format size information is incorrect; compensating")
				}
				skip, err = field.parseTypeName(field.TypeName, true, -1)
				if err != nil {
					return true, err
				}
				if skip {
					return true, nil
				}
				field.arraySize = field.Size / field.dataTypeSize
			}
		} else {
			field.arraySize = field.Size / field.dataTypeSize
		}
	}

	return false, nil
}

func parseTraceEventField(line string) (*traceEventField, error) {
	var err error
	var fieldString string

	field := &traceEventField{}
	fields := strings.Split(strings.TrimSpace(line), ";")
	for i := 0; i < len(fields); i++ {
		if fields[i] == "" {
			continue
		}
		parts := strings.Split(fields[i], ":")
		if len(parts) != 2 {
			return nil, errors.New("malformed format field")
		}

		switch strings.TrimSpace(parts[0]) {
		case "field":
			fieldString = parts[1]
		case "offset":
			field.Offset, err = strconv.Atoi(parts[1])
		case "size":
			field.Size, err = strconv.Atoi(parts[1])
		case "signed":
			field.IsSigned, err = strconv.ParseBool(parts[1])
		}
		if err != nil {
			return nil, err
		}
	}

	skip, err := field.parseTypeAndName(fieldString)
	if err != nil {
		return nil, err
	}
	if skip {
		// If a field is marked as skip, treat it as an array of bytes
		field.dataTypeSize = 1
		field.arraySize = field.Size
		if field.IsSigned {
			field.dataType = TraceEventFieldTypeSignedInt8
		} else {
			field.dataType = TraceEventFieldTypeUnsignedInt8
		}
	}
	return field, nil
}

func getTraceEventFormat(tracingDir, name string) (uint16, map[string]traceEventField, error) {
	filename := filepath.Join(tracingDir, "events", name, "format")
	file, err := os.OpenFile(filename, os.O_RDONLY, 0)
	if err != nil {
		return 0, nil, err
	}
	defer file.Close()

	return readTraceEventFormat(name, file)
}

func readTraceEventFormat(name string, reader io.Reader) (uint16, map[string]traceEventField, error) {
	var eventID uint16

	inFormat := false
	fields := make(map[string]traceEventField)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		rawLine := scanner.Text()
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		if inFormat {
			if !unicode.IsSpace(rune(rawLine[0])) {
				inFormat = false
				continue
			}
			field, err := parseTraceEventField(line)
			if err != nil {
				glog.Infof("Couldn't parse trace event format: %v", err)
				return 0, nil, err
			}
			if field != nil {
				fields[field.FieldName] = *field
			}
		} else if strings.HasPrefix(line, "format:") {
			inFormat = true
		} else if strings.HasPrefix(line, "ID:") {
			value := strings.TrimSpace(line[3:])
			parsedValue, err := strconv.Atoi(value)
			if err != nil {
				glog.Infof("Couldn't parse trace event ID: %v", err)
				return 0, nil, err
			}
			eventID = uint16(parsedValue)
		}
	}
	err := scanner.Err()
	if err != nil {
		return 0, nil, err
	}

	return eventID, fields, err
}
