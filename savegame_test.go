package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// buildSave lays out a save the way the game does around the plugin list: a TESV
// header, a placeholder screenshot, then the length-prefixed UTF-16 plugin names.
// The exact header fields before that are deliberately not needed by the reader,
// which is the point of locating the list by its own marker.
func buildSave(plugins ...string) []byte {
	var b bytes.Buffer
	b.WriteString("TESV")
	b.Write(make([]byte, 12))       // header size, version, save number
	b.Write(make([]byte, 96))       // player name/level/location placeholders
	b.WriteByte(12)                 // form version
	b.Write(make([]byte, 4))        // plugin info size
	b.WriteByte(byte(len(plugins))) // plugin count
	for _, p := range plugins {
		units := utf16.Encode([]rune(p))
		var n [2]byte
		binary.LittleEndian.PutUint16(n[:], uint16(len(units)))
		b.Write(n[:])
		for _, u := range units {
			var c [2]byte
			binary.LittleEndian.PutUint16(c[:], u)
			b.Write(c[:])
		}
	}
	b.Write([]byte{0, 0}) // empty string ends the list
	b.Write(make([]byte, 8))
	return b.Bytes()
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *fixture) save(name string, plugins ...string) {
	f.t.Helper()
	f.mkdir("profiles/Default/saves")
	full := filepath.Join(f.dir, "profiles", "Default", "saves", name)
	if err := os.WriteFile(full, buildSave(plugins...), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) runWithSaves(opts Options) *Result {
	f.t.Helper()
	opts.SaveDirs = []string{filepath.Join(f.dir, "profiles", "Default", "saves")}
	return f.runWith(opts)
}

func TestReadSavePlugins(t *testing.T) {
	got, err := readSavePlugins(writeTemp(t, "Save.ess", buildSave("Skyrim.esm", "Update.esm", "Mod.esp")))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := []string{"Skyrim.esm", "Update.esm", "Mod.esp"}
	if len(got) != len(want) {
		t.Fatalf("plugins = %v, want %v", got, want)
	}
	for i := range want {
		if !strings.EqualFold(got[i], want[i]) {
			t.Errorf("plugin %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Random bytes that are not a save must be rejected quietly, never half-read.
func TestReadSavePluginsRejectsJunk(t *testing.T) {
	if _, err := readSavePlugins(writeTemp(t, "junk.ess", []byte("not a save at all"))); err == nil {
		t.Fatal("junk should not parse as a save")
	}
	// TESV but nothing resembling a plugin list.
	noList := append([]byte("TESV"), make([]byte, 64)...)
	if _, err := readSavePlugins(writeTemp(t, "empty.ess", noList)); err == nil {
		t.Fatal("a save with no plugin list should be rejected")
	}
}

// A save needing a plugin that is no longer installed is the usual crash after a
// removal, so it is reported as a warning naming the save.
func TestAuditReportsSavePluginMissing(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Keep", "Keep.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modlist("+Keep")
	f.plugins("*Skyrim.esm", "*Keep.esp")
	f.save("Save1.ess", "Skyrim.esm", "Removed.esp")

	res := f.runWithSaves(Options{})
	fd := findFinding(res, "SAVE_PLUGIN_MISSING", "Removed.esp")
	if fd == nil {
		t.Fatalf("want SAVE_PLUGIN_MISSING, got %+v", res.Findings)
	}
	if fd.Severity != SevWarn {
		t.Errorf("severity = %q, want warn (a new game still starts)", fd.Severity)
	}
	if !strings.Contains(fd.Detail, "Save1.ess") || !strings.Contains(fd.Detail, "nothing installed ships it") {
		t.Errorf("detail should name the save and say the file is gone: %q", fd.Detail)
	}
	if fd.Search == "" {
		t.Error("a removed plugin should be searchable on Nexus")
	}
	if res.SaveFile == "" || res.SavePlugins != 2 {
		t.Errorf("save bookkeeping: file=%q plugins=%d", res.SaveFile, res.SavePlugins)
	}
}

// A plugin that still exists in a disabled mod is a different fix (enable it), so
// the detail says which mod ships it.
func TestAuditSavePluginInstalledButDisabled(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("OldMod", "Old.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modlist("-OldMod")
	f.plugins("*Skyrim.esm")
	f.save("Save2.ess", "Skyrim.esm", "Old.esp")

	res := f.runWithSaves(Options{})
	fd := findFinding(res, "SAVE_PLUGIN_MISSING", "Old.esp")
	if fd == nil {
		t.Fatalf("want SAVE_PLUGIN_MISSING, got %+v", res.Findings)
	}
	if !strings.Contains(fd.Detail, "OldMod") {
		t.Errorf("detail should name the disabled mod: %q", fd.Detail)
	}
}

// With no saves at all the audit says nothing: no finding, no error, empty save
// fields. This is the normal state of a fresh install.
func TestAuditWithoutSavesIsQuiet(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Keep", "Keep.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modlist("+Keep")
	f.plugins("*Skyrim.esm", "*Keep.esp")

	res := f.runWithSaves(Options{})
	if fd := findFinding(res, "SAVE_PLUGIN_MISSING", ""); fd != nil {
		t.Errorf("no saves should mean no finding: %+v", fd)
	}
	if res.SaveFile != "" || res.SavePlugins != 0 {
		t.Errorf("save fields should be empty: %q %d", res.SaveFile, res.SavePlugins)
	}
	if res.Counts.Error != 0 || res.Counts.Warn != 0 {
		t.Errorf("counts = %+v", res.Counts)
	}
}

// A profile's own saves beat the Documents folder even when a Documents save is
// newer: that folder can hold another character's save from before the profile
// took saves over.
func TestProfileSavesTakePrecedence(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Keep", "Keep.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modlist("+Keep")
	f.plugins("*Skyrim.esm", "*Keep.esp")
	f.save("Profile.ess", "Skyrim.esm", "Keep.esp")

	other := t.TempDir()
	p := filepath.Join(other, "OtherCharacter.ess")
	if err := os.WriteFile(p, buildSave("Skyrim.esm", "Ancient.esp"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}

	res := f.runWith(Options{SaveDirs: []string{
		filepath.Join(f.dir, "profiles", "Default", "saves"), other,
	}})
	if fd := findFinding(res, "SAVE_PLUGIN_MISSING", "Ancient.esp"); fd != nil {
		t.Errorf("a save from another folder must not be reported: %+v", fd)
	}
	if !strings.HasSuffix(res.SaveFile, "Profile.ess") {
		t.Errorf("save file = %q, want Profile.ess", res.SaveFile)
	}
}

// Only the newest save is inspected: an old save from another character must not
// turn into findings.
func TestAuditUsesNewestSaveOnly(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Keep", "Keep.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modlist("+Keep")
	f.plugins("*Skyrim.esm", "*Keep.esp")
	f.save("Old.ess", "Skyrim.esm", "Ancient.esp")
	f.save("New.ess", "Skyrim.esm")

	// Pin the timestamps instead of trusting filesystem resolution.
	base := time.Now().Add(-2 * time.Hour)
	for name, at := range map[string]time.Time{"Old.ess": base, "New.ess": base.Add(time.Hour)} {
		p := filepath.Join(f.dir, "profiles", "Default", "saves", name)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}

	res := f.runWithSaves(Options{})
	if fd := findFinding(res, "SAVE_PLUGIN_MISSING", "Ancient.esp"); fd != nil {
		t.Errorf("an older save must not be reported: %+v", fd)
	}
	if !strings.HasSuffix(res.SaveFile, "New.ess") {
		t.Errorf("save file = %q, want New.ess", res.SaveFile)
	}
}
