package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Removing a mod has to leave the profile loadable: folder moved aside,
// modlist entry unchecked, its plugins gone from plugins.txt and loadorder.txt,
// and a backup of the files it rewrote.
func TestRemoveModQuarantinesAndCleansProfile(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Keep", "Keep.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 2))
	f.modPlugin("Bad", "Bad.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 2))
	f.modlist("+Keep", "+Bad")
	f.plugins("*Skyrim.esm", "*Keep.esp", "*Bad.esp")
	f.loadOrder("Skyrim.esm", "Keep.esp", "Bad.esp")

	inst, err := OpenInstance(f.dir, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	out, err := RemoveMod(inst, "Bad", "quarantine")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out.NewPath == "" || !pathExists(out.NewPath) {
		t.Fatalf("mod folder was not quarantined: %+v", out)
	}
	if pathExists(filepath.Join(inst.ModsDir, "Bad")) {
		t.Error("original mod folder still exists")
	}
	if !strings.Contains(out.NewPath, quarantineDirName) {
		t.Errorf("quarantine path = %q", out.NewPath)
	}

	modlist := readText(t, inst.ModListTxt)
	if !strings.Contains(modlist, "-Bad") || strings.Contains(modlist, "+Bad") {
		t.Errorf("modlist.txt = %q, want Bad disabled", modlist)
	}
	if !strings.Contains(modlist, "+Keep") {
		t.Errorf("modlist.txt lost the untouched mod: %q", modlist)
	}

	plugins := readText(t, inst.PluginsTxt)
	if strings.Contains(plugins, "Bad.esp") {
		t.Errorf("plugins.txt still enables the removed plugin: %q", plugins)
	}
	if !strings.Contains(plugins, "*Keep.esp") {
		t.Errorf("plugins.txt lost an unrelated plugin: %q", plugins)
	}
	order := readText(t, inst.LoadOrderTxt)
	if strings.Contains(order, "Bad.esp") {
		t.Errorf("loadorder.txt still lists the removed plugin: %q", order)
	}
	if len(out.UncheckedPlugin) != 1 || out.UncheckedPlugin[0] != "Bad.esp" {
		t.Errorf("unchecked = %v, want [Bad.esp]", out.UncheckedPlugin)
	}
	if len(out.Backups) != 3 {
		t.Errorf("backups = %v, want plugins.txt, modlist.txt and loadorder.txt", out.Backups)
	}
	for _, bak := range out.Backups {
		if !pathExists(bak) {
			t.Errorf("missing backup %s", bak)
		}
	}
}

func TestRemoveModDeletesFolder(t *testing.T) {
	f := newFixture(t)
	f.modPlugin("Bad", "Bad.esp", buildPlugin(0, 1.7, nil, 1))
	f.modlist("+Bad")
	f.plugins("*Bad.esp")

	inst, err := OpenInstance(f.dir, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	out, err := RemoveMod(inst, "Bad", "delete")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if out.Mode != "delete" {
		t.Errorf("mode = %q", out.Mode)
	}
	if pathExists(filepath.Join(inst.ModsDir, "Bad")) {
		t.Error("mod folder still exists after delete")
	}
	if pathExists(filepath.Join(inst.ModsDir, quarantineDirName)) {
		t.Error("delete mode should not create a quarantine folder")
	}
}

// A crafted name must never touch anything outside the mods folder.
func TestRemoveModRejectsPathTraversal(t *testing.T) {
	f := newFixture(t)
	f.plugins("*Skyrim.esm")
	inst, err := OpenInstance(f.dir, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, name := range []string{"..", "../profiles", "a/b", `C:\Windows`} {
		if _, err := RemoveMod(inst, name, "delete"); err == nil {
			t.Errorf("RemoveMod(%q) should be rejected", name)
		}
	}
	if !pathExists(filepath.Join(inst.Dir, "profiles", "Default")) {
		t.Error("profile folder was touched")
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
