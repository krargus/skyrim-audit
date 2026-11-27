package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Mo2Instance is the subset of a Mod Organizer 2 instance that auditing needs.
type Mo2Instance struct {
	Dir          string // instance folder (holds ModOrganizer.ini)
	BaseDir      string // value of [Settings] base_directory
	ModsDir      string
	ProfilesDir  string
	Profile      string
	ProfileDir   string
	PluginsTxt   string
	ModListTxt   string
	LoadOrderTxt string
	GameData     string // <gamePath>/Data, may be empty when unknown
}

// OpenInstance resolves an MO2 instance folder and one of its profiles. dir may
// also be a profile folder (the one holding plugins.txt); the instance is then
// derived from it.
func OpenInstance(dir, profileFlag string) (*Mo2Instance, error) {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("cannot open instance folder: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("not a folder: %s", abs)
	}

	inst := &Mo2Instance{Dir: abs, BaseDir: abs}

	iniPath := filepath.Join(abs, "ModOrganizer.ini")
	if _, statErr := os.Stat(iniPath); statErr == nil {
		text, err := os.ReadFile(iniPath)
		if err != nil {
			return nil, fmt.Errorf("cannot read ModOrganizer.ini: %w", err)
		}
		ini := parseIni(string(text))
		settings := ini["Settings"]
		if v := settings["base_directory"]; v != "" {
			inst.BaseDir = resolvePath(abs, v)
		}
		inst.ModsDir = settings["mod_directory"]
		inst.ProfilesDir = settings["profiles_directory"]
		inst.Profile = ini["General"]["selected_profile"]
		// MO2 writes this as gamePath; older hand-edited inis use game_path.
		if gp := firstNonEmpty(ini["General"]["gamePath"], ini["General"]["game_path"]); gp != "" {
			inst.GameData = filepath.Join(resolvePath(abs, gp), "Data")
		}
	} else if _, statErr := os.Stat(filepath.Join(abs, "plugins.txt")); statErr == nil {
		// Given a profile folder directly.
		inst.ProfileDir = abs
		inst.Profile = filepath.Base(abs)
		inst.ProfilesDir = filepath.Dir(abs)
		inst.BaseDir = filepath.Dir(inst.ProfilesDir)
	} else {
		return nil, fmt.Errorf("%s is not an MO2 instance folder (no ModOrganizer.ini)", abs)
	}

	if inst.ModsDir == "" {
		inst.ModsDir = filepath.Join(inst.BaseDir, "mods")
	} else {
		inst.ModsDir = resolvePath(inst.BaseDir, inst.ModsDir)
	}
	if inst.ProfilesDir == "" {
		inst.ProfilesDir = filepath.Join(inst.BaseDir, "profiles")
	} else {
		inst.ProfilesDir = resolvePath(inst.BaseDir, inst.ProfilesDir)
	}

	if profileFlag != "" {
		inst.Profile = profileFlag
	}
	if inst.Profile == "" {
		inst.Profile = singleProfile(inst.ProfilesDir)
	}
	if inst.Profile == "" {
		return nil, fmt.Errorf("no profile selected: pass -profile, or open a profile once in MO2")
	}

	inst.ProfileDir = filepath.Join(inst.ProfilesDir, inst.Profile)
	inst.PluginsTxt = filepath.Join(inst.ProfileDir, "plugins.txt")
	inst.ModListTxt = filepath.Join(inst.ProfileDir, "modlist.txt")
	inst.LoadOrderTxt = filepath.Join(inst.ProfileDir, "loadorder.txt")

	if _, err := os.Stat(inst.PluginsTxt); err != nil {
		return nil, fmt.Errorf("profile %q has no plugins.txt at %s", inst.Profile, inst.PluginsTxt)
	}
	return inst, nil
}

// SetGameData overrides the game Data folder, for instances whose ini has no
// game_path (or when the game lives somewhere else).
func (m *Mo2Instance) SetGameData(dir string) {
	if dir == "" {
		return
	}
	if abs, err := filepath.Abs(dir); err == nil {
		m.GameData = abs
	}
}

// modMeta is the Nexus identity MO2 records for an installed mod.
type modMeta struct {
	NexusID int
}

// readModMeta reads the [General] block of a mod's meta.ini, where MO2 stores
// the Nexus mod id. Best effort: hand-installed mods have none.
func readModMeta(dir string) modMeta {
	data, err := os.ReadFile(filepath.Join(dir, "meta.ini"))
	if err != nil {
		return modMeta{}
	}
	general := parseIni(string(data))["General"]
	var m modMeta
	if id, err := strconv.Atoi(strings.TrimSpace(general["modid"])); err == nil && id > 0 {
		m.NexusID = id
	}
	return m
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func singleProfile(profilesDir string) string {
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		return ""
	}
	names := make([]string, 0, 2)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(profilesDir, e.Name(), "plugins.txt")); err == nil {
			names = append(names, e.Name())
		}
	}
	if len(names) == 1 {
		return names[0]
	}
	return ""
}

// parseIni handles the QSettings-style ini MO2 writes: [Section] + key=value.
func parseIni(text string) map[string]map[string]string {
	ini := map[string]map[string]string{"General": {}}
	section := "General"
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			if ini[section] == nil {
				ini[section] = map[string]string{}
			}
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		ini[section][key] = unwrapQSettings(strings.TrimSpace(line[eq+1:]))
	}
	return ini
}

// unwrapQSettings removes the wrappers QSettings uses for non-string values.
// MO2 writes selected_profile as @ByteArray(Default), so the profile name has to
// be unwrapped before it can be used as a folder name.
func unwrapQSettings(val string) string {
	v := strings.Trim(strings.TrimSpace(val), "\"")
	for _, prefix := range []string{"@ByteArray(", "@Variant("} {
		if strings.HasPrefix(v, prefix) && strings.HasSuffix(v, ")") {
			v = strings.Trim(strings.TrimSuffix(strings.TrimPrefix(v, prefix), ")"), "\"")
		}
	}
	return v
}

// resolvePath expands %BASE_DIR% and makes relative values absolute against base.
func resolvePath(base, value string) string {
	v := strings.ReplaceAll(value, "%BASE_DIR%", base)
	v = filepath.FromSlash(strings.ReplaceAll(v, "\\", "/"))
	if !filepath.IsAbs(v) {
		v = filepath.Join(base, v)
	}
	return filepath.Clean(v)
}

// PluginEntry is one line of plugins.txt. MO2 writes an asterisk in front of
// every enabled plugin.
type PluginEntry struct {
	Name    string
	Enabled bool
}

// ReadPluginsTxt parses a profile's plugins.txt, keeping file order because that
// order is the load order.
func ReadPluginsTxt(path string) ([]PluginEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parsePluginsTxt(string(data)), nil
}

func parsePluginsTxt(text string) []PluginEntry {
	var out []PluginEntry
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry := PluginEntry{Name: line, Enabled: false}
		if strings.HasPrefix(line, "*") {
			entry.Name = strings.TrimSpace(line[1:])
			entry.Enabled = true
		}
		if entry.Name == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// ReadLoadOrder parses a profile's loadorder.txt, which lists every plugin the
// engine sees in load order: base masters and Creation Club content included.
// MO2 only tracks mod plugins in plugins.txt, so this file is what makes master
// checks resolve against Skyrim.esm and the DLC.
func ReadLoadOrder(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "*")))
	}
	return out, nil
}

// ModEntry is one line of modlist.txt. MO2 writes it highest priority first, and
// prefixes enabled mods with '+' and disabled ones with '-'.
type ModEntry struct {
	Name    string
	Enabled bool
}

// ReadModList parses a profile's modlist.txt in priority order (first is highest,
// which is what wins loose-file conflicts).
func ReadModList(path string) ([]ModEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseModList(string(data)), nil
}

func parseModList(text string) []ModEntry {
	var mods []ModEntry
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		marker := line[0]
		if marker != '+' && marker != '-' && marker != '*' {
			continue
		}
		name := strings.TrimSpace(line[1:])
		if name == "" || strings.HasSuffix(strings.ToLower(name), "_separator") {
			continue
		}
		mods = append(mods, ModEntry{Name: name, Enabled: marker != '-'})
	}
	return mods
}
