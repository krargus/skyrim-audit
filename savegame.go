package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// maxSavePrefix bounds how much of a save is read. The plugin list follows the
// header and the screenshot, so it is always near the front; a save whose list we
// cannot find in this window is skipped rather than half-parsed.
const maxSavePrefix = 8 << 20

// SaveGame is the part of a save that decides whether a load order can still load
// it: the plugins it was made with.
type SaveGame struct {
	Path    string
	ModTime time.Time
	Plugins []string
}

// latestSave finds the most recently written save for the profile: MO2's
// profile-specific saves first, then the game's Documents folder. No save, or an
// unreadable one, is not an error - the audit simply has nothing to say.
func latestSave(m *Mo2Instance, override []string) *SaveGame {
	dirs := override
	if len(dirs) == 0 {
		dirs = saveDirs(m)
	}
	// The folders are in precedence order: a profile's own saves are the ones the
	// game is writing, while Documents can still hold saves from before the profile
	// took them over, or from another character. The first folder with a readable
	// save wins outright rather than competing on timestamp.
	for _, dir := range dirs {
		save := newestInDir(dir)
		if save == nil {
			continue
		}
		plugins, err := readSavePlugins(save.Path)
		if err != nil || len(plugins) == 0 {
			continue
		}
		save.Plugins = plugins
		return save
	}
	return nil
}

// newestInDir returns the most recently written .ess in one folder, without
// reading it. Zero-byte .ess.tmp leftovers from a crash are ignored.
func newestInDir(dir string) *SaveGame {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var best *SaveGame
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".ess") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() == 0 {
			continue
		}
		if best != nil && !info.ModTime().After(best.ModTime) {
			continue
		}
		best = &SaveGame{Path: filepath.Join(dir, e.Name()), ModTime: info.ModTime()}
	}
	return best
}

// saveDirs lists the folders a profile's saves can live in. MO2 redirects saves
// into the profile when "profile-specific saves" is enabled; otherwise they sit
// in the game's Documents folder, which OneDrive often owns.
func saveDirs(m *Mo2Instance) []string {
	var dirs []string
	if m.ProfileDir != "" {
		dirs = append(dirs, filepath.Join(m.ProfileDir, "saves"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, "Documents", "My Games", "Skyrim Special Edition", "Saves"),
			filepath.Join(home, "OneDrive", "Documents", "My Games", "Skyrim Special Edition", "Saves"),
		)
	}
	for _, env := range []string{"OneDrive", "OneDriveConsumer"} {
		if base := os.Getenv(env); base != "" {
			dirs = append(dirs, filepath.Join(base, "Documents", "My Games", "Skyrim Special Edition", "Saves"))
		}
	}
	return dirs
}

// readSavePlugins returns the plugin list a save was made with. The list starts
// with Skyrim.esm, so it is found by its UTF-16 marker rather than by walking
// every header field, which differs between game versions and save tools. The
// list is validated before it is believed, so random bytes in a screenshot cannot
// pass for plugins.
func readSavePlugins(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxSavePrefix))
	if err != nil {
		return nil, err
	}
	if len(data) < recordHeaderSize || string(data[0:4]) != "TESV" {
		return nil, fmt.Errorf("not a Skyrim save")
	}

	off := pluginListStart(data)
	if off < 0 {
		return nil, fmt.Errorf("no plugin list in the save header")
	}
	var out []string
	for off+2 <= len(data) {
		n := int(binary.LittleEndian.Uint16(data[off : off+2]))
		off += 2
		if n == 0 || n > 260 || off+2*n > len(data) {
			break
		}
		name := decodeUTF16(data[off : off+2*n])
		off += 2 * n
		if !isPluginFile(name) {
			break
		}
		out = append(out, name)
	}
	if len(out) == 0 || !strings.EqualFold(out[0], "Skyrim.esm") {
		return nil, fmt.Errorf("plugin list not found in the save")
	}
	return out, nil
}

// pluginListStart finds the length-prefixed UTF-16 "Skyrim.esm" that opens the
// plugin list.
func pluginListStart(data []byte) int {
	needle := encodeUTF16("Skyrim.esm")
	pat := make([]byte, 0, len(needle)+2)
	pat = append(pat, byte(len("Skyrim.esm")), 0)
	pat = append(pat, needle...)
	if i := bytes.Index(data, pat); i >= 0 {
		return i
	}
	return -1
}

func encodeUTF16(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(units)*2)
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func decodeUTF16(b []byte) string {
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	runes := utf16.Decode(units)
	// Trim trailing NULs some writers include inside the length.
	return strings.TrimRight(string(runes), "\x00")
}

// reportSaveDependencies names the plugins the save needs that this profile no
// longer loads: that is the usual reason a save crashes right after a mod was
// removed.
func reportSaveDependencies(res *Result, m *Mo2Instance, refs map[string][]pluginRef, enabledIdx map[string]int, save *SaveGame) {
	saveName := filepath.Base(save.Path)
	seen := map[string]bool{}
	for _, name := range save.Plugins {
		key := strings.ToLower(name)
		if _, loaded := enabledIdx[key]; loaded || seen[key] {
			continue
		}
		seen[key] = true
		detail := fmt.Sprintf("save %q was made with this plugin, and it is not loaded now", saveName)
		if path, source, shippedBy := m.locate(refs, name); path == "" {
			detail += " - nothing installed ships it any more"
		} else if strings.HasPrefix(source, "mod:") {
			detail += fmt.Sprintf(" - it is shipped by %s, which is unchecked", strings.Join(shippedBy, ", "))
		} else {
			detail += " - it is in the game Data folder but not enabled in this load order"
		}
		res.add(SevWarn, "SAVE_PLUGIN_MISSING", name, detail)
	}
}
