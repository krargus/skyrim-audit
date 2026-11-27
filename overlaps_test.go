package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestIsDocFile(t *testing.T) {
	docs := []string{
		"license", "License.txt", "LICENCE", "readme.txt", "README - Skyrim.txt",
		"readme_english", "version.txt", "GameVersion.txt", "changelog.md",
		"CREDITS.txt", "install.txt", "docs.html", "manual.PDF", "Thumbs.db",
	}
	for _, name := range docs {
		if !isDocFile(name) {
			t.Errorf("isDocFile(%q) = false, want true", name)
		}
	}

	assets := []string{
		"meshes/actors/character/character assets/femalebody_1.nif",
		"textures/armor/iron/cuirass.dds",
		"skse/plugins/cbpconfig.txt",
		"scripts/racemenumorphscbbeuimenubase.pex",
		"sound/fx/step.wav",
		"interface/translations/skyrim_english.txt",
	}
	for _, name := range assets {
		if isDocFile(name) {
			t.Errorf("isDocFile(%q) = true, want false", name)
		}
	}
}

// Counts and conflicts come out of the same walk. A disabled mod is still
// counted but cannot win, a folder with no content files still gets a row, and a
// repeated modlist entry is one row with its files counted once.
func TestScanCountsAndOverlapsInOnePass(t *testing.T) {
	f := newFixture(t)
	f.mkdir("mods/High/meshes")
	f.mkdir("mods/Low/meshes")
	f.mkdir("mods/Off/meshes")
	f.mkdir("mods/Empty")
	f.write("mods/High/meshes/a.nif", "x")
	f.write("mods/High/meshes/b.nif", "x")
	f.write("mods/Low/meshes/a.nif", "x")
	f.write("mods/Low/readme.txt", "docs")
	f.write("mods/Low/Low.esp", "plugin")
	f.write("mods/Off/meshes/c.nif", "x")
	mods := []ModEntry{
		{Name: "High", Enabled: true}, {Name: "Low", Enabled: true},
		{Name: "Off"}, {Name: "Empty", Enabled: true}, {Name: "High", Enabled: true},
	}

	scan := scanModFiles(filepath.Join(f.dir, "mods"), mods)
	for mod, want := range map[string]int{"High": 2, "Low": 1, "Off": 1, "Empty": 0} {
		if got := scan.Counts[mod]; got != want {
			t.Errorf("%s files = %d, want %d", mod, got, want)
		}
	}
	if want := []string{"High", "Low", "Off", "Empty"}; strings.Join(scan.Order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", scan.Order, want)
	}
	if len(scan.Overlaps) != 1 {
		t.Fatalf("overlaps = %+v, want exactly High over Low", scan.Overlaps)
	}
	ov := scan.Overlaps[0]
	if ov.Winner != "High" || ov.Loser != "Low" || ov.Files != 1 {
		t.Errorf("overlap = %+v, want High over Low, 1 file", ov)
	}
	if len(ov.Examples) != 1 || ov.Examples[0] != "meshes/a.nif" {
		t.Errorf("examples = %v", ov.Examples)
	}
}

// Once the file budget is spent the walk stops, but the mods it never reached
// must still be listed: a missing row reads as an uninstalled mod, a zero count
// does not.
func TestScanKeepsModsAfterBudgetExhausted(t *testing.T) {
	f := newFixture(t)
	f.mkdir("mods/First")
	f.mkdir("mods/Later")
	f.write("mods/First/a.nif", "x")
	f.write("mods/First/b.nif", "x")
	f.write("mods/First/c.nif", "x")
	f.write("mods/Later/d.nif", "x")
	mods := []ModEntry{{Name: "First", Enabled: true}, {Name: "Later", Enabled: true}}

	scan := scanModFilesWithLimits(filepath.Join(f.dir, "mods"), mods, scanLimits{PerMod: 10, Total: 2})
	if got := scan.Counts["First"]; got != 2 {
		t.Errorf("First files = %d, want the 2 the budget allowed", got)
	}
	if got, ok := scan.Counts["Later"]; !ok || got != 0 {
		t.Errorf("Later = %d (present=%v), want 0 and listed", got, ok)
	}
	if want := []string{"First", "Later"}; strings.Join(scan.Order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", scan.Order, want)
	}
	if len(scan.Overlaps) != 0 {
		t.Errorf("a file the budget skipped cannot conflict: %+v", scan.Overlaps)
	}
}

// An addon that masters another mod overrides that mod's files on purpose, so it
// must not be reported as a conflict.
func TestAuditSuppressesOverlapRequiredByWinner(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Base", "Base.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 2))
	f.modPlugin("Addon", "Addon.esp", buildPlugin(0, 1.7, []string{"Base.esp"}, 2))
	f.mkdir("mods/Base/meshes")
	f.mkdir("mods/Addon/meshes")
	f.write("mods/Base/meshes/shared.nif", "mesh")
	f.write("mods/Addon/meshes/shared.nif", "mesh")
	f.modlist("+Addon", "+Base")
	f.plugins("*Skyrim.esm", "*Base.esp", "*Addon.esp")

	res := f.run()
	if hasCode(res, "OVERLAP") {
		t.Fatalf("addon overriding its master must not be a finding: %+v", res.Findings)
	}
	if len(res.ExpectedOverlaps) != 1 {
		t.Fatalf("expected_overlaps = %+v, want one", res.ExpectedOverlaps)
	}
	note := res.ExpectedOverlaps[0]
	if note.Winner != "Addon" || note.Loser != "Base" {
		t.Errorf("winner/loser = %s/%s", note.Winner, note.Loser)
	}
	if !strings.Contains(note.Reason, "Addon.esp") {
		t.Errorf("reason = %q, want the mastering plugin named", note.Reason)
	}
}

// Documentation overlaps are noise; a real asset overlap must still be reported.
func TestAuditIgnoresDocumentationOverlaps(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.mkdir("mods/A/meshes")
	f.mkdir("mods/B/meshes")
	f.write("mods/A/meshes/sword.nif", "mesh")
	f.write("mods/B/meshes/sword.nif", "mesh")
	f.write("mods/A/license", "licence text")
	f.write("mods/B/license", "licence text")
	f.write("mods/A/readme.txt", "readme")
	f.write("mods/B/readme.txt", "readme")
	f.write("mods/A/version.txt", "1.0")
	f.write("mods/B/version.txt", "2.0")
	f.modlist("+A", "+B")
	f.plugins("*Skyrim.esm")

	res := f.run()
	var overlaps []Finding
	for _, fd := range res.Findings {
		if fd.Code == "OVERLAP" {
			overlaps = append(overlaps, fd)
		}
	}
	if len(overlaps) != 1 {
		t.Fatalf("want exactly one asset overlap, got %d: %+v", len(overlaps), overlaps)
	}
	got := overlaps[0]
	if !strings.Contains(got.Detail, "meshes/sword.nif") {
		t.Errorf("overlap does not mention the asset: %q", got.Detail)
	}
	for _, word := range []string{"license", "readme", "version.txt"} {
		if strings.Contains(strings.ToLower(got.Detail), word) {
			t.Errorf("overlap still counts %s: %q", word, got.Detail)
		}
	}
}
