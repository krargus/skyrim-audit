package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// GameVersion is the installed Skyrim build, read from SkyrimSE.exe.
//
// Which files a mod page offers depends on this number: SKSE, Address Library,
// .NET Script Framework and various DLL mods are compiled per executable build,
// and the light-plugin FormID budget changed with 1.6.1130. Reporting it lets
// the user and the Nexus search pick the right file for their game.
type GameVersion struct {
	Path     string `json:"path,omitempty"`
	Version  string `json:"version,omitempty"`
	Edition  string `json:"edition,omitempty"`
	Major    int    `json:"major,omitempty"`
	Minor    int    `json:"minor,omitempty"`
	Patch    int    `json:"patch,omitempty"`
	Build    int    `json:"build,omitempty"`
	EslLimit int    `json:"esl_limit"`
	Detected bool   `json:"detected"`
	Note     string `json:"note,omitempty"`
}

// vsFixedFileInfoSig is the start of the VS_FIXEDFILEINFO structure that every
// built executable keeps in its version resource.
var vsFixedFileInfoSig = []byte{0xBD, 0x04, 0xEF, 0xFE} // 0xFEEF04BD

// DetectGameVersion reads SkyrimSE.exe from the folder above the Data folder.
// gameData may be empty; the result then reports Detected=false and the default
// light-plugin limit.
func DetectGameVersion(gameData string) GameVersion {
	v := GameVersion{EslLimit: defaultEslRecordLimit}
	if gameData == "" {
		v.Note = "game Data folder unknown, so the game version was not checked; assuming 1.6.1130+ (light plugins hold 4096 records)"
		return v
	}
	exe := filepath.Join(filepath.Dir(gameData), "SkyrimSE.exe")
	if st, err := os.Stat(exe); err != nil || st.IsDir() {
		v.Note = "SkyrimSE.exe not found next to the Data folder, so the game version was not checked; assuming 1.6.1130+ (pass -esl-limit 2048 on 1.5.97 or 1.6.640)"
		return v
	}
	v.Path = exe
	nums, err := readPEFileVersion(exe)
	if err != nil {
		v.Note = "could not read the SkyrimSE.exe version (" + err.Error() + "); assuming 1.6.1130+"
		return v
	}
	v.Major, v.Minor, v.Patch, v.Build = nums[0], nums[1], nums[2], nums[3]
	v.Version = versionString(nums)
	v.Detected = true
	v.Edition = editionFor(nums)
	if atLeast(nums, 1, 6, 1130) {
		v.EslLimit = 4096
	} else {
		v.EslLimit = 2048
	}
	v.Note = "mod files must match this exact build; SKSE, Address Library and DLL plugins are compiled per version"
	return v
}

func versionString(n [4]int) string {
	return itoa(n[0]) + "." + itoa(n[1]) + "." + itoa(n[2]) + "." + itoa(n[3])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func atLeast(n [4]int, major, minor, patch int) bool {
	switch {
	case n[0] != major:
		return n[0] > major
	case n[1] != minor:
		return n[1] > minor
	default:
		return n[2] >= patch
	}
}

func editionFor(n [4]int) string {
	switch {
	case n[0] == 1 && n[1] == 5:
		return "Special Edition (pre-AE)"
	case atLeast(n, 1, 6, 1130):
		return "Anniversary Edition"
	case n[0] == 1 && n[1] == 6:
		return "Special Edition (post-AE update)"
	}
	return "unknown branch"
}

// readPEFileVersion pulls the fixed file version out of the PE version resource.
// It scans for VS_FIXEDFILEINFO rather than walking the resource tree, which
// keeps it dependency-free and works for every Bethesda executable.
func readPEFileVersion(path string) ([4]int, error) {
	var out [4]int
	data, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	for off := 0; ; {
		i := bytes.Index(data[off:], vsFixedFileInfoSig)
		if i < 0 {
			return out, errNoVersionResource
		}
		at := off + i
		if at+16 <= len(data) {
			ms := binary.LittleEndian.Uint32(data[at+8:])
			ls := binary.LittleEndian.Uint32(data[at+12:])
			out = [4]int{int(ms >> 16), int(ms & 0xFFFF), int(ls >> 16), int(ls & 0xFFFF)}
			if out[0] > 0 && out[0] < 100 && out[2] < 100000 {
				return out, nil
			}
		}
		off = at + len(vsFixedFileInfoSig)
	}
}

var errNoVersionResource = errors.New("no VS_FIXEDFILEINFO in the executable")

// Summary is a one-line description for reports.
func (v GameVersion) Summary() string {
	if !v.Detected {
		return "not detected"
	}
	return "Skyrim " + v.Version + " (" + strings.TrimSpace(v.Edition) + ")"
}
