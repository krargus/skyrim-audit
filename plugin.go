package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// TES4 record header flags.
const (
	flagMaster = 0x00000001
	flagLight  = 0x00000200
	flagMedium = 0x00000400
)

// Record header is 20 bytes: type, dataSize, flags, formID, versionControl.
const (
	recordHeaderSize = 20
	groupHeaderSize  = 24
)

var errNotPlugin = errors.New("not a TES4 plugin (missing TES4 signature)")

// Plugin is the part of a .esp/.esm/.esl header that load-order auditing needs.
type Plugin struct {
	Name        string
	Path        string
	Version     float32
	Master      bool
	Light       bool
	Medium      bool
	Masters     []string
	HeaderCount uint32
	RecordCount int
}

// Kind is a short human label for the plugin's flag combination.
func (p *Plugin) Kind() string {
	switch {
	case p.Light && p.Medium:
		return "light+medium"
	case p.Light:
		return "light"
	case p.Medium:
		return "medium"
	case p.Master:
		return "master"
	default:
		return "plugin"
	}
}

// ReadPluginHeader loads and parses a plugin file.
func ReadPluginHeader(path string) (*Plugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParsePluginHeader(filepath.Base(path), path, data)
}

// ParsePluginHeader parses TES4 header records from raw plugin bytes and counts
// the records that follow, so light-plugin capacity can be checked.
func ParsePluginHeader(name, path string, data []byte) (*Plugin, error) {
	if len(data) < recordHeaderSize || string(data[0:4]) != "TES4" {
		return nil, errNotPlugin
	}
	p := &Plugin{Name: name, Path: path}

	size := int(binary.LittleEndian.Uint32(data[4:8]))
	flags := binary.LittleEndian.Uint32(data[8:12])
	p.Master = flags&flagMaster != 0
	p.Light = flags&flagLight != 0
	p.Medium = flags&flagMedium != 0

	// Skyrim SE (like FO4 and later) writes 24-byte record headers: the usual
	// 20 bytes plus a 2-byte form version and a second version-control field.
	// Older games use 20. The first subrecord is HEDR either way, so locating
	// it tells us which layout this file uses.
	hdr := detectHeaderSize(data)

	end := hdr + size
	if end > len(data) || end < hdr {
		return nil, fmt.Errorf("truncated TES4 header: declares %d bytes, file has %d", end, len(data))
	}

	sub := data[hdr:end]
	for off := 0; off+6 <= len(sub); {
		typ := string(sub[off : off+4])
		sz := int(binary.LittleEndian.Uint16(sub[off+4 : off+6]))
		off += 6
		if off+sz > len(sub) {
			break
		}
		body := sub[off : off+sz]
		off += sz
		switch typ {
		case "MAST":
			p.Masters = append(p.Masters, cstring(body))
		case "HEDR":
			if len(body) >= 12 {
				p.Version = math.Float32frombits(binary.LittleEndian.Uint32(body[0:4]))
				p.HeaderCount = binary.LittleEndian.Uint32(body[4:8])
			}
		}
	}

	count, err := countRecords(data[end:], hdr)
	p.RecordCount = count
	return p, err
}

// detectHeaderSize returns 24 for Skyrim SE-style records and 20 for older ones.
func detectHeaderSize(data []byte) int {
	if len(data) >= 28 && string(data[24:28]) == "HEDR" {
		return 24
	}
	return 20
}

// countRecords walks the group tree that follows the TES4 header. A group's
// dataSize includes its own 24-byte header; a record's dataSize does not.
func countRecords(data []byte, recHeader int) (int, error) {
	total := 0

	var walk func(b []byte, depth, base int) error
	walk = func(b []byte, depth, base int) error {
		if depth > 32 {
			return errors.New("group nesting deeper than 32 levels")
		}
		off := 0
		for off+recHeader <= len(b) {
			typ := string(b[off : off+4])
			size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))

			if typ == "GRUP" {
				if size < groupHeaderSize || off+size > len(b) {
					return fmt.Errorf("bad group size %d at file offset %d", size, base+off)
				}
				if err := walk(b[off+groupHeaderSize:off+size], depth+1, base+off+groupHeaderSize); err != nil {
					return err
				}
				off += size
				continue
			}
			if size < 0 || off+recHeader+size > len(b) {
				return fmt.Errorf("bad record size %d (type %q) at file offset %d", size, typ, base+off)
			}
			total++
			off += recHeader + size
		}
		return nil
	}

	if len(data) == 0 {
		return 0, nil
	}
	err := walk(data, 0, 0)
	return total, err
}

// cstring returns bs up to the first NUL, which is how MAST stores a filename.
func cstring(bs []byte) string {
	if i := strings.IndexByte(string(bs), 0); i >= 0 {
		return string(bs[:i])
	}
	return string(bs)
}

func isPluginFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".esp", ".esm", ".esl":
		return true
	}
	return false
}
