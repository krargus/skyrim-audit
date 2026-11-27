package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appendSub(b []byte, typ string, body []byte) []byte {
	h := make([]byte, 6)
	copy(h[0:4], typ)
	binary.LittleEndian.PutUint16(h[4:6], uint16(len(body)))
	b = append(b, h...)
	return append(b, body...)
}

// buildPlugin produces a minimal but structurally valid TES4 plugin.
func buildPlugin(flags uint32, version float32, masters []string, records int) []byte {
	var sub []byte
	hedr := make([]byte, 12)
	binary.LittleEndian.PutUint32(hedr[0:4], math.Float32bits(version))
	binary.LittleEndian.PutUint32(hedr[4:8], uint32(records))
	binary.LittleEndian.PutUint32(hedr[8:12], 0x800)
	sub = appendSub(sub, "HEDR", hedr)
	for _, m := range masters {
		sub = appendSub(sub, "MAST", append([]byte(m), 0))
		sub = appendSub(sub, "DATA", make([]byte, 8))
	}

	out := make([]byte, 20)
	copy(out[0:4], "TES4")
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(sub)))
	binary.LittleEndian.PutUint32(out[8:12], flags)
	out = append(out, sub...)

	if records > 0 {
		body := make([]byte, 0, records*20)
		for i := 0; i < records; i++ {
			rec := make([]byte, 20)
			copy(rec[0:4], "WEAP")
			body = append(body, rec...)
		}
		g := make([]byte, 24)
		copy(g[0:4], "GRUP")
		binary.LittleEndian.PutUint32(g[4:8], uint32(24+len(body)))
		copy(g[8:12], "WEAP")
		out = append(out, g...)
		out = append(out, body...)
	}
	return out
}

func TestParsePluginHeader(t *testing.T) {
	data := buildPlugin(flagMaster|flagLight, 1.71, []string{"Skyrim.esm", "Update.esm"}, 3)
	p, err := ParsePluginHeader("Test.esp", "Test.esp", data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.Master || !p.Light || p.Medium {
		t.Errorf("flags: master=%v light=%v medium=%v", p.Master, p.Light, p.Medium)
	}
	if len(p.Masters) != 2 || p.Masters[1] != "Update.esm" {
		t.Errorf("masters = %v", p.Masters)
	}
	if p.RecordCount != 3 {
		t.Errorf("record count = %d, want 3", p.RecordCount)
	}
	if p.Version < 1.709 || p.Version > 1.711 {
		t.Errorf("version = %v, want 1.71", p.Version)
	}
	if got := p.Kind(); got != "light" {
		t.Errorf("kind = %q", got)
	}
}

// The light-plugin record cap belongs to the game build, not the plugin header:
// official Creation Club ESLs use 1.70 headers and exceed 2048 records.
func TestEslLimitComesFromGameNotHeader(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("CC", "ccTest.esl", buildPlugin(flagLight, 1.7, []string{"Skyrim.esm"}, 2100))
	f.modlist("+CC")
	f.plugins("*Skyrim.esm", "*ccTest.esl")

	modern := f.run()
	if modern.Counts.Error != 0 {
		t.Errorf("2100 records should be fine on a 4096-capable game: %+v", modern.Findings)
	}

	old := f.runWith(Options{EslRecordLimit: 2048})
	if !hasCode(old, "LIGHT_OVER_CAPACITY") {
		t.Errorf("want LIGHT_OVER_CAPACITY at -esl-limit 2048: %+v", old.Findings)
	}
}

func TestCountRecordsNestedGroups(t *testing.T) {
	// One record at top level, one inside a group, one inside a nested group.
	rec := func() []byte {
		r := make([]byte, 20)
		copy(r[0:4], "WEAP")
		return r
	}
	inner := rec()
	nested := make([]byte, 24)
	copy(nested[0:4], "GRUP")
	binary.LittleEndian.PutUint32(nested[4:8], uint32(24+len(inner)))
	nested = append(nested, inner...)

	mid := rec()
	midGroup := make([]byte, 24)
	copy(midGroup[0:4], "GRUP")
	binary.LittleEndian.PutUint32(midGroup[4:8], uint32(24+len(mid)+len(nested)))
	midGroup = append(midGroup, mid...)
	midGroup = append(midGroup, nested...)

	data := rec()
	data = append(data, midGroup...)

	n, err := countRecords(data, recordHeaderSize)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("records = %d, want 3", n)
	}
}

// buildSSEPlugin builds a plugin the way Skyrim SE lays it out: 24-byte record
// headers (20 bytes plus form version and a second version-control field).
func buildSSEPlugin(flags uint32, version float32, masters []string, records int) []byte {
	var sub []byte
	heldr := make([]byte, 12)
	binary.LittleEndian.PutUint32(heldr[0:4], math.Float32bits(version))
	binary.LittleEndian.PutUint32(heldr[4:8], uint32(records))
	binary.LittleEndian.PutUint32(heldr[8:12], 0x800)
	sub = appendSub(sub, "HEDR", heldr)
	for _, m := range masters {
		sub = appendSub(sub, "MAST", append([]byte(m), 0))
		sub = appendSub(sub, "DATA", make([]byte, 8))
	}

	out := make([]byte, 24)
	copy(out[0:4], "TES4")
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(sub)))
	binary.LittleEndian.PutUint32(out[8:12], flags)
	binary.LittleEndian.PutUint16(out[20:22], 44)
	out = append(out, sub...)

	if records > 0 {
		body := make([]byte, 0, records*24)
		for i := 0; i < records; i++ {
			rec := make([]byte, 24)
			copy(rec[0:4], "WEAP")
			binary.LittleEndian.PutUint16(rec[20:22], 44)
			body = append(body, rec...)
		}
		g := make([]byte, 24)
		copy(g[0:4], "GRUP")
		binary.LittleEndian.PutUint32(g[4:8], uint32(24+len(body)))
		copy(g[8:12], "WEAP")
		out = append(out, g...)
		out = append(out, body...)
	}
	return out
}

func TestParseSSEHeader24Byte(t *testing.T) {
	p, err := ParsePluginHeader("SE.esp", "SE.esp", buildSSEPlugin(flagLight, 1.7, []string{"Skyrim.esm"}, 7))
	if err != nil {
		t.Fatalf("parse SSE-layout plugin: %v", err)
	}
	if p.RecordCount != 7 {
		t.Errorf("record count = %d, want 7", p.RecordCount)
	}
	if !p.Light {
		t.Error("light flag not detected")
	}
	if len(p.Masters) != 1 || p.Masters[0] != "Skyrim.esm" {
		t.Errorf("masters = %v", p.Masters)
	}
	if p.Version < 1.699 || p.Version > 1.711 {
		t.Errorf("version = %v, want 1.7", p.Version)
	}
}

func TestParsePluginsTxt(t *testing.T) {
	text := "\ufeff# comment\r\n*Skyrim.esm\r\nUpdate.esm\r\n*Mod.esp\r\n\r\n"
	entries := parsePluginsTxt(text)
	if len(entries) != 3 {
		t.Fatalf("entries = %d: %+v", len(entries), entries)
	}
	if !entries[0].Enabled || entries[0].Name != "Skyrim.esm" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].Enabled || entries[1].Name != "Update.esm" {
		t.Errorf("entry 1 = %+v", entries[1])
	}
	if !entries[2].Enabled || entries[2].Name != "Mod.esp" {
		t.Errorf("entry 2 = %+v", entries[2])
	}
}

func TestParseIniUnwrapsQSettingsValues(t *testing.T) {
	ini := parseIni("[General]\nselected_profile=@ByteArray(Default)\ngame_path=\"C:/Games/Skyrim\"\n")
	if got := ini["General"]["selected_profile"]; got != "Default" {
		t.Errorf("selected_profile = %q, want Default", got)
	}
	if got := ini["General"]["game_path"]; got != "C:/Games/Skyrim" {
		t.Errorf("game_path = %q", got)
	}
}

func TestOpenInstanceReadsCamelCaseGamePath(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "Skyrim Special Edition")
	if err := os.MkdirAll(filepath.Join(game, "Data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "profiles", "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles", "Default", "plugins.txt"), []byte("*Skyrim.esm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	escaped := strings.ReplaceAll(filepath.ToSlash(game), "/", "\\\\")
	ini := "[General]\ngameName=Skyrim Special Edition\nselected_profile=@ByteArray(Default)\ngamePath=@ByteArray(" + escaped + ")\n"
	if err := os.WriteFile(filepath.Join(dir, "ModOrganizer.ini"), []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}

	inst, err := OpenInstance(dir, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if inst.Profile != "Default" {
		t.Errorf("profile = %q", inst.Profile)
	}
	want := filepath.Join(game, "Data")
	if inst.GameData != want {
		t.Errorf("game data = %q, want %q", inst.GameData, want)
	}
}

func TestParseModListPriorityOrder(t *testing.T) {
	text := "+HighPriority\r\n-LowDisabled\r\n+_separator\r\n"
	mods := parseModList(text)
	if len(mods) != 2 {
		t.Fatalf("mods = %d: %+v", len(mods), mods)
	}
	if mods[0].Name != "HighPriority" || !mods[0].Enabled {
		t.Errorf("mod 0 = %+v", mods[0])
	}
	if mods[1].Name != "LowDisabled" || mods[1].Enabled {
		t.Errorf("mod 1 = %+v", mods[1])
	}
}

type fixture struct {
	t   *testing.T
	dir string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, dir: dir}
	f.mkdir("profiles/Default")
	f.mkdir("mods")
	f.mkdir("game/Data")
	gamePath := filepath.ToSlash(filepath.Join(dir, "game"))
	f.write("ModOrganizer.ini", "[General]\nselected_profile=Default\ngame_path="+gamePath+"\n")
	return f
}

func (f *fixture) mkdir(rel string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.dir, filepath.FromSlash(rel)), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	full := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) modPlugin(mod, name string, data []byte) {
	f.t.Helper()
	f.mkdir("mods/" + mod)
	full := filepath.Join(f.dir, "mods", mod, name)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) dataPlugin(name string, data []byte) {
	f.t.Helper()
	full := filepath.Join(f.dir, "game", "Data", name)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) plugins(lines ...string) {
	f.write("profiles/Default/plugins.txt", join(lines))
}

func (f *fixture) modlist(lines ...string) {
	f.write("profiles/Default/modlist.txt", join(lines))
}

func join(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func (f *fixture) run() *Result {
	f.t.Helper()
	return f.runWith(Options{})
}

func (f *fixture) runWith(opts Options) *Result {
	f.t.Helper()
	inst, err := OpenInstance(f.dir, "")
	if err != nil {
		f.t.Fatalf("open instance: %v", err)
	}
	res, err := Audit(inst, opts)
	if err != nil {
		f.t.Fatalf("audit: %v", err)
	}
	return res
}

func hasCode(r *Result, code string) bool {
	for _, f := range r.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func TestAuditCleanLoadOrder(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("BaseMod", "Base.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 5))
	f.modlist("+BaseMod")
	f.plugins("*Skyrim.esm", "*Base.esp")

	res := f.run()
	if res.Counts.Error != 0 || res.Counts.Warn != 0 {
		t.Fatalf("expected clean, got %+v: %+v", res.Counts, res.Findings)
	}
	if res.Enabled != 2 || res.Full != 2 || res.Light != 0 {
		t.Errorf("counts: enabled=%d full=%d light=%d", res.Enabled, res.Full, res.Light)
	}
}

func (f *fixture) loadOrder(lines ...string) {
	f.write("profiles/Default/loadorder.txt", join(lines))
}

// Base-game masters and Creation Club content live in loadorder.txt but not in
// plugins.txt; they must still count as loaded, or every plugin looks broken.
func TestAuditLoadOrderProvidesBaseMasters(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.dataPlugin("Update.esm", buildPlugin(flagMaster, 1.7, []string{"Skyrim.esm"}, 2))
	f.modPlugin("M", "Mod.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Update.esm"}, 3))
	f.modlist("+M")
	f.loadOrder("Skyrim.esm", "Update.esm", "Mod.esp")
	f.plugins("*Mod.esp")

	res := f.run()
	if res.Counts.Error != 0 {
		t.Fatalf("expected no errors, got %+v", res.Findings)
	}
	if res.Enabled != 3 {
		t.Errorf("enabled = %d, want 3 (base masters included)", res.Enabled)
	}
}

func TestAuditMissingMaster(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("M", "Needy.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Gone.esp"}, 3))
	f.modlist("+M")
	f.plugins("*Skyrim.esm", "*Needy.esp")

	res := f.run()
	if !hasCode(res, "MASTER_MISSING") {
		t.Fatalf("want MASTER_MISSING, got %+v", res.Findings)
	}
	if res.Counts.Error == 0 {
		t.Error("missing master must be an error")
	}
}

func TestAuditMasterOutOfOrder(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("M", "Base.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modPlugin("M", "Dependent.esp", buildPlugin(0, 1.7, []string{"Base.esp"}, 3))
	f.modlist("+M")
	f.plugins("*Skyrim.esm", "*Dependent.esp", "*Base.esp")

	res := f.run()
	if !hasCode(res, "MASTER_OUT_OF_ORDER") {
		t.Fatalf("want MASTER_OUT_OF_ORDER, got %+v", res.Findings)
	}
}

func TestAuditPluginMissing(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("First", "Present.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modlist("+First")
	f.plugins("*Skyrim.esm", "*Present.esp", "*Ghost.esp")

	res := f.run()
	if !hasCode(res, "PLUGIN_MISSING") {
		t.Errorf("want PLUGIN_MISSING for Ghost.esp, got %+v", res.Findings)
	}
}

// Disabling the losing mod is one of the two remedies the warning offers, so it
// has to clear the warning: a disabled mod is never mounted, and its copy can
// neither win the file nor fight over it later.
func TestAuditDuplicatePluginClearsWhenLoserDisabled(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("First", "Shared.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modPlugin("Second", "Shared.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 4))
	f.modlist("+First", "+Second")
	f.plugins("*Skyrim.esm", "*Shared.esp")

	if !hasCode(f.run(), "DUPLICATE_PLUGIN") {
		t.Fatal("two enabled copies must warn before the loser is disabled")
	}

	f.modlist("+First", "-Second")
	res := f.run()
	if hasCode(res, "DUPLICATE_PLUGIN") {
		t.Errorf("disabling the losing mod must clear the warning: %+v", res.Findings)
	}
	if res.Counts.Error != 0 {
		t.Errorf("the winner must still provide the plugin: %+v", res.Findings)
	}
}

func TestAuditLightOverCapacity(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("M", "Big.esl", buildPlugin(flagLight, 1.7, []string{"Skyrim.esm"}, 4300))
	f.modlist("+M")
	f.plugins("*Skyrim.esm", "*Big.esl")

	res := f.run()
	if !hasCode(res, "LIGHT_OVER_CAPACITY") {
		t.Fatalf("want LIGHT_OVER_CAPACITY, got %+v", res.Findings)
	}
	if res.Light != 1 || res.Full != 1 {
		t.Errorf("counts: full=%d light=%d", res.Full, res.Light)
	}
}

func TestAuditLightAtNewLimitIsFine(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("M", "Big.esl", buildPlugin(flagLight, 1.71, []string{"Skyrim.esm"}, 3900))
	f.modlist("+M")
	f.plugins("*Skyrim.esm", "*Big.esl")

	res := f.run()
	if hasCode(res, "LIGHT_OVER_CAPACITY") {
		t.Fatalf("3000 records should fit a 1.71 light plugin: %+v", res.Findings)
	}
}

func TestOpenInstanceFromProfileFolder(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.plugins("*Skyrim.esm")

	inst, err := OpenInstance(filepath.Join(f.dir, "profiles", "Default"), "")
	if err != nil {
		t.Fatalf("open from profile folder: %v", err)
	}
	if inst.Profile != "Default" {
		t.Errorf("profile = %q", inst.Profile)
	}
	if inst.ModsDir != filepath.Join(f.dir, "mods") {
		t.Errorf("mods dir = %q", inst.ModsDir)
	}
}
