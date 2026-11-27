package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Only plugins whose master is installed nowhere may be offered for unchecking.
// A plugin whose master is merely unchecked loads once that mod is enabled, so
// disabling it would hide the real fix.
func TestFixCandidatesSplitMissingAndUnchecked(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Base", "Base.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modPlugin("M", "AbsentChild.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Gone.esp"}, 3))
	f.modPlugin("M", "UncheckedChild.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Base.esp"}, 3))
	f.modlist("-Base", "+M")
	f.plugins("*Skyrim.esm", "*AbsentChild.esp", "*UncheckedChild.esp")

	res := f.run()
	if !containsString(res.BrokenPlugins, "AbsentChild.esp") || !containsString(res.BrokenPlugins, "UncheckedChild.esp") {
		t.Fatalf("both plugins cannot load: %v", res.BrokenPlugins)
	}
	if !containsString(res.DisableCandidates, "AbsentChild.esp") {
		t.Errorf("a master installed nowhere must be a disable candidate: %v", res.DisableCandidates)
	}
	if containsString(res.DisableCandidates, "UncheckedChild.esp") {
		t.Errorf("a merely unchecked master must not be a disable candidate: %v", res.DisableCandidates)
	}
}

// One unrepairable master is enough, and header order must not decide it: a
// plugin with an unchecked master and a missing one cannot be fixed by enabling.
func TestFixCandidatesAnyUnrepairableMasterWins(t *testing.T) {
	orders := [][]string{
		{"Base.esp", "Gone.esp"},
		{"Gone.esp", "Base.esp"},
	}
	for _, masters := range orders {
		f := newFixture(t)
		f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
		f.modPlugin("Base", "Base.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
		f.modPlugin("M", "Needy.esp", buildPlugin(0, 1.7, append([]string{"Skyrim.esm"}, masters...), 3))
		f.modlist("-Base", "+M")
		f.plugins("*Skyrim.esm", "*Needy.esp")

		res := f.run()
		if !containsString(res.DisableCandidates, "Needy.esp") {
			t.Errorf("masters %v: a missing master must make the plugin a candidate, got %v", masters, res.DisableCandidates)
		}
	}
}

// Classification must keep up with chains discovered later: a plugin that is
// first broken only by an unchecked master becomes unrepairable once the rest of
// its chain resolves to a missing plugin.
func TestFixCandidatesUpgradeAfterChain(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Base", "Base.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modPlugin("M", "Top.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Base.esp", "Bridge.esp"}, 3))
	f.modPlugin("M", "Bridge.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Deep.esp"}, 3))
	f.modPlugin("M", "Deep.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Gone.esp"}, 3))
	f.modlist("-Base", "+M")
	f.plugins("*Skyrim.esm", "*Top.esp", "*Bridge.esp", "*Deep.esp")

	res := f.run()
	for _, want := range []string{"Top.esp", "Bridge.esp", "Deep.esp"} {
		if !containsString(res.DisableCandidates, want) {
			t.Errorf("%s depends on the missing plugin and must be a candidate, got %v", want, res.DisableCandidates)
		}
	}
	if !containsString(res.BrokenPlugins, "Top.esp") {
		t.Errorf("Top.esp cannot load: %v", res.BrokenPlugins)
	}
}

// The split follows the chain: a plugin whose broken master is itself unrepairable
// inherits that, and a plugin whose master only needs enabling does not.
func TestFixCandidatesFollowTheChain(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Base", "Base.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modPlugin("M", "AbsentRoot.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Gone.esp"}, 3))
	f.modPlugin("M", "AbsentChild.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "AbsentRoot.esp"}, 3))
	f.modPlugin("M", "UncheckedChild.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Base.esp"}, 3))
	f.modPlugin("M", "UncheckedGrandchild.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "UncheckedChild.esp"}, 3))
	f.modlist("-Base", "+M")
	f.plugins("*Skyrim.esm", "*AbsentRoot.esp", "*AbsentChild.esp", "*UncheckedChild.esp", "*UncheckedGrandchild.esp")

	res := f.run()
	for _, want := range []string{"AbsentRoot.esp", "AbsentChild.esp"} {
		if !containsString(res.DisableCandidates, want) {
			t.Errorf("%s should be a disable candidate: %v", want, res.DisableCandidates)
		}
	}
	for _, notWant := range []string{"UncheckedChild.esp", "UncheckedGrandchild.esp"} {
		if containsString(res.DisableCandidates, notWant) {
			t.Errorf("%s should not be a disable candidate: %v", notWant, res.DisableCandidates)
		}
	}
}

// A plugin that masters a broken one is broken too, and the audit has to say so:
// reporting only the first link leaves a fix list that still will not start.
func TestAuditMissingMasterChain(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("M", "Needy.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Gone.esp"}, 3))
	f.modPlugin("M", "Dependent.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Needy.esp"}, 3))
	f.modPlugin("M", "Leaf.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Dependent.esp"}, 3))
	f.modlist("+M")
	f.plugins("*Skyrim.esm", "*Needy.esp", "*Dependent.esp", "*Leaf.esp")

	res := f.run()
	if !hasCode(res, "MASTER_MISSING") {
		t.Fatalf("want MASTER_MISSING, got %+v", res.Findings)
	}
	var broken []string
	for _, name := range res.BrokenPlugins {
		broken = append(broken, strings.ToLower(name))
	}
	for _, want := range []string{"needy.esp", "dependent.esp", "leaf.esp"} {
		if !containsString(broken, want) {
			t.Errorf("BrokenPlugins = %v, want %s included", res.BrokenPlugins, want)
		}
	}
	dependent := findFinding(res, "MASTER_BROKEN", "Dependent.esp")
	if dependent == nil || !strings.Contains(dependent.Detail, "Needy.esp") {
		t.Errorf("MASTER_BROKEN should name the master it depends on: %+v", dependent)
	}
}

// The missing-master message has to distinguish "in no mod at all" from "in a
// disabled mod", because one is a download and the other is a checkbox.
func TestAuditMasterDiagnosisNamesTheMod(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Parent", "Parent.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modPlugin("Child", "Child.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Parent.esp"}, 3))
	f.modlist("-Parent", "+Child")
	f.plugins("*Skyrim.esm", "*Child.esp")

	res := f.run()
	fd := findFinding(res, "MASTER_DISABLED", "Child.esp")
	if fd == nil {
		t.Fatalf("want MASTER_DISABLED, got %+v", res.Findings)
	}
	if !strings.Contains(fd.Detail, "Parent") {
		t.Errorf("detail should name the mod shipping the master: %q", fd.Detail)
	}

	missing := findFinding(res, "MASTER_MISSING", "Child.esp")
	if missing != nil {
		t.Errorf("a master that exists in a disabled mod is not missing: %+v", missing)
	}
}

// A master can be switched off while the mod shipping it is already on. Telling
// the user to enable that mod is advice that cannot be followed and would not
// change anything, so the finding must point at the plugin switch instead, and
// the dependent must stay off the disable list because that switch can fix it.
func TestAuditMasterUntickedInEnabledMod(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Base", "Parent.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modPlugin("M", "Child.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Parent.esp"}, 3))
	f.modlist("+Base", "+M")
	f.plugins("*Skyrim.esm", "Parent.esp", "*Child.esp")

	res := f.run()
	fd := findFinding(res, "MASTER_UNCHECKED", "Child.esp")
	if fd == nil {
		t.Fatalf("want MASTER_UNCHECKED for Child.esp, got %+v", res.Findings)
	}
	if !strings.Contains(fd.Detail, "Parent") {
		t.Errorf("detail should name the switch that repairs it: %q", fd.Detail)
	}
	if containsString(res.DisableCandidates, "Child.esp") {
		t.Errorf("switching the master on can repair Child.esp, so it must not be a disable candidate: %v", res.DisableCandidates)
	}

	// Do what the finding says, and the warning has to clear.
	f.plugins("*Skyrim.esm", "*Parent.esp", "*Child.esp")
	if res = f.run(); res.Counts.Error != 0 {
		t.Errorf("after switching the master on there is nothing left to fix: %+v", res.Findings)
	}
}

// Switching a master back on only helps if that master could load. When its own
// master is installed nowhere, the dependent cannot be repaired by a checkbox,
// so it must be classified as unrepairable and the missing link named.
func TestAuditUntickedMasterThatCannotLoad(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Base", "Parent.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Gone.esp"}, 3))
	f.modPlugin("M", "Child.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Parent.esp"}, 3))
	f.modlist("+Base", "+M")
	f.plugins("*Skyrim.esm", "Parent.esp", "*Child.esp")

	res := f.run()
	fd := findFinding(res, "MASTER_UNCHECKED_BROKEN", "Child.esp")
	if fd == nil {
		t.Fatalf("want MASTER_UNCHECKED_BROKEN for Child.esp, got %+v", res.Findings)
	}
	if !strings.Contains(fd.Detail, "Gone.esp") {
		t.Errorf("detail should name what the master is missing: %q", fd.Detail)
	}
	if !containsString(res.DisableCandidates, "Child.esp") {
		t.Errorf("a master that cannot load must make Child.esp a disable candidate: %v", res.DisableCandidates)
	}
}

// An enabled plugin whose only copy lives in an unchecked mod is not loaded by
// MO2, so the audit must say so instead of reading the file and moving on. The
// remedy is enabling that mod, so it is never a disable candidate.
func TestAuditPluginOnlyInDisabledMod(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("OnlyDisabled", "Orphan.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modlist("-OnlyDisabled")
	f.plugins("*Skyrim.esm", "*Orphan.esp")

	res := f.run()
	fd := findFinding(res, "PLUGIN_IN_DISABLED_MOD", "Orphan.esp")
	if fd == nil {
		t.Fatalf("want PLUGIN_IN_DISABLED_MOD, got %+v", res.Findings)
	}
	if !strings.Contains(fd.Detail, "OnlyDisabled") {
		t.Errorf("detail should name the disabled mod: %q", fd.Detail)
	}
	if !containsString(res.BrokenPlugins, "Orphan.esp") {
		t.Errorf("broken = %v, want Orphan.esp", res.BrokenPlugins)
	}
	if containsString(res.DisableCandidates, "Orphan.esp") {
		t.Errorf("enabling the mod repairs it, so it is not a candidate: %v", res.DisableCandidates)
	}
}

// A base-game master present on disk but absent from loadorder.txt is a
// load-order problem, not a download.
func TestAuditMasterInDataButNotEnabled(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.dataPlugin("Update.esm", buildPlugin(flagMaster, 1.7, []string{"Skyrim.esm"}, 2))
	f.modPlugin("M", "Needs.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Update.esm"}, 3))
	f.modlist("+M")
	f.plugins("*Skyrim.esm", "*Needs.esp")

	res := f.run()
	fd := findFinding(res, "MASTER_DISABLED", "Needs.esp")
	if fd == nil || !strings.Contains(fd.Detail, "Data folder") {
		t.Fatalf("want MASTER_DISABLED naming the Data folder, got %+v", res.Findings)
	}
}

// A compacted plugin changes its FormIDs, so advice to compact one is only safe
// when nothing can depend on it - which cannot be proven offline (saves, patches
// not installed yet). The audit therefore offers no such advice at all.
func TestAuditGivesNoEslAdvice(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("M", "Small.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 40))
	f.modlist("+M")
	f.plugins("*Skyrim.esm", "*Small.esp")

	res := f.run()
	if fd := findFinding(res, "ESL_CANDIDATES", ""); fd != nil {
		t.Fatalf("no ESL-ification advice should be emitted: %+v", fd)
	}
}

// The fix action must uncheck only what the audit marked broken and must leave the
// file loadable by MO2: names stay, the asterisk goes.
func TestDisablePluginsUnchecksBrokenOnly(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("M", "Needy.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Gone.esp"}, 3))
	f.modPlugin("M", "Fine.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modlist("+M")
	f.plugins("*Skyrim.esm", "*Needy.esp", "*Fine.esp")

	inst, err := OpenInstance(f.dir, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	res := f.run()
	out, err := DisablePlugins(inst, res.BrokenPlugins)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if len(out.Disabled) != 1 || out.Disabled[0] != "Needy.esp" {
		t.Errorf("disabled = %v, want [Needy.esp]", out.Disabled)
	}
	if out.Backup == "" || !pathExists(out.Backup) {
		t.Errorf("plugins.txt was not backed up: %+v", out)
	}

	text := readText(t, inst.PluginsTxt)
	if !strings.Contains(text, "\nNeedy.esp") || strings.Contains(text, "*Needy.esp") {
		t.Errorf("Needy.esp should stay listed but unchecked: %q", text)
	}
	if !strings.Contains(text, "*Fine.esp") || !strings.Contains(text, "*Skyrim.esm") {
		t.Errorf("unrelated plugins must stay enabled: %q", text)
	}

	after := f.run()
	if hasCode(after, "MASTER_MISSING") || len(after.BrokenPlugins) != 0 {
		t.Errorf("audit still reports the fixed error: %+v", after.Findings)
	}
}

// A name that is not an enabled plugin entry must never be written into the file.
func TestDisablePluginsSkipsUnknownNames(t *testing.T) {
	f := newFixture(t)
	f.plugins("*Skyrim.esm")
	inst, err := OpenInstance(f.dir, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	out, err := DisablePlugins(inst, []string{"NotInstalled.esp"})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if len(out.Skipped) != 1 || out.Skipped[0] != "NotInstalled.esp" {
		t.Errorf("skipped = %v", out.Skipped)
	}
	text := readText(t, inst.PluginsTxt)
	if strings.Contains(text, "NotInstalled.esp") {
		t.Errorf("unknown plugin was written into plugins.txt: %q", text)
	}
}

// Removing a mod and putting it back must leave the profile as it was: folder,
// modlist entry and plugin lines all restored.
func TestQuarantineRestoreRoundTrip(t *testing.T) {
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
	if _, err := RemoveMod(inst, "Bad", "quarantine"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	list := ListRemovals(inst)
	if len(list) != 1 || list[0].Mod != "Bad" {
		t.Fatalf("journal = %+v, want Bad", list)
	}
	if len(list[0].UncheckedPlugins) != 1 || list[0].UncheckedPlugins[0] != "Bad.esp" {
		t.Errorf("journal plugins = %v", list[0].UncheckedPlugins)
	}

	out, err := RestoreMod(inst, "Bad")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if out.Mode != "restore" || !pathExists(filepath.Join(inst.ModsDir, "Bad")) {
		t.Errorf("folder did not come back: %+v", out)
	}
	modlist := readText(t, inst.ModListTxt)
	if !strings.Contains(modlist, "+Bad") || strings.Contains(modlist, "-Bad") {
		t.Errorf("modlist.txt = %q, want +Bad", modlist)
	}
	plugins := readText(t, inst.PluginsTxt)
	if !strings.Contains(plugins, "*Bad.esp") {
		t.Errorf("plugins.txt = %q, want *Bad.esp", plugins)
	}
	if got := ListRemovals(inst); len(got) != 0 {
		t.Errorf("journal still lists the restored mod: %+v", got)
	}
}

// Restore must refuse to overwrite a mod folder the user recreated meanwhile.
func TestRestoreRefusesExistingFolder(t *testing.T) {
	f := newFixture(t)
	f.modPlugin("Bad", "Bad.esp", buildPlugin(0, 1.7, nil, 1))
	f.modlist("+Bad")
	f.plugins("*Bad.esp")
	inst, err := OpenInstance(f.dir, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := RemoveMod(inst, "Bad", "quarantine"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(inst.ModsDir, "Bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreMod(inst, "Bad"); err == nil {
		t.Fatal("restore should refuse to overwrite an existing folder")
	}
}

// The journal is editable by hand, so restore must not follow a crafted entry
// out of the quarantine folder.
func TestRestoreRejectsCraftedJournal(t *testing.T) {
	f := newFixture(t)
	f.plugins("*Skyrim.esm")
	inst, err := OpenInstance(f.dir, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inst.ModsDir, quarantineDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rec := range []RemovalRecord{
		{Mod: "..", NewPath: filepath.Join(inst.ModsDir, quarantineDirName, "x")},
		{Mod: "Bad", NewPath: outside},
	} {
		if err := writeJournal(inst, []RemovalRecord{rec}); err != nil {
			t.Fatal(err)
		}
		if _, err := RestoreMod(inst, rec.Mod); err == nil {
			t.Errorf("restore accepted crafted journal entry %+v", rec)
		}
	}
	entries, err := os.ReadDir(filepath.Join(inst.ModsDir, quarantineDirName))
	if err != nil || len(entries) != 0 {
		t.Errorf("quarantine should still be empty, got %v (err=%v)", entries, err)
	}
}

func TestHTMLReportEscapesAndListsFindings(t *testing.T) {
	res := &Result{
		Instance: `C:\Modding\MO2`,
		Profile:  "Default",
		Counts:   Counts{Error: 1},
		Findings: []Finding{{
			Severity: SevError, Code: "MASTER_MISSING", Plugin: "A&B.esp",
			Detail: "master <Gone.esp> is missing", Fix: "install it",
		}},
		BrokenPlugins: []string{"A&B.esp"},
	}
	page := res.HTMLReport()
	if !strings.Contains(page, "A&amp;B.esp") {
		t.Error("plugin name was not HTML-escaped")
	}
	if strings.Contains(page, "<Gone.esp>") {
		t.Error("detail was not HTML-escaped")
	}
	if !strings.Contains(page, "MASTER_MISSING") || !strings.Contains(page, "Plugins that cannot load") {
		t.Error("report is missing findings or the broken list")
	}

	path := filepath.Join(t.TempDir(), "report.html")
	if err := res.WriteHTMLReport(path); err != nil {
		t.Fatalf("write: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) == 0 {
		t.Errorf("report file empty (err=%v)", err)
	}
}

func findFinding(res *Result, code, plugin string) *Finding {
	for i := range res.Findings {
		if res.Findings[i].Code == code && (plugin == "" || res.Findings[i].Plugin == plugin) {
			return &res.Findings[i]
		}
	}
	return nil
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Every finding whose advice is an action the user can take here has to clear
// when that action is taken. The duplicate-plugin and switched-off-master bugs
// both shipped advice that left the warning standing; this table makes that
// class of trap fail a test instead of a user. Rows already exercised end to end
// by a dedicated test (MASTER_UNCHECKED, DUPLICATE_PLUGIN) are not repeated here,
// and OVERLAP is absent because its advice is that nothing needs doing.
//
// Where the advice says "untick" or "disable the plugin", the row leaves the
// entry in plugins.txt without the asterisk, which is exactly what MO2 writes for
// a disabled plugin. Deleting the line instead would be a different action.
func TestActionableAdviceClearsFinding(t *testing.T) {
	base := func(f *fixture) { f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2)) }

	cases := []struct {
		name   string
		code   string
		plugin string
		opts   Options
		setup  func(*fixture)
		act    func(*fixture)
	}{
		{
			name: "MASTER_MISSING", code: "MASTER_MISSING", plugin: "Needy.esp",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("M", "Needy.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Gone.esp"}, 3))
				f.modlist("+M")
				f.plugins("*Skyrim.esm", "*Needy.esp")
			},
			act: func(f *fixture) { f.plugins("*Skyrim.esm", "Needy.esp") },
		},
		{
			name: "MASTER_DISABLED shipped by a disabled mod", code: "MASTER_DISABLED", plugin: "Child.esp",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("Base", "Parent.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
				f.modPlugin("M", "Child.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Parent.esp"}, 3))
				f.modlist("-Base", "+M")
				f.plugins("*Skyrim.esm", "*Child.esp")
			},
			act: func(f *fixture) {
				f.modlist("+Base", "+M")
				f.plugins("*Skyrim.esm", "*Parent.esp", "*Child.esp")
			},
		},
		{
			name: "MASTER_DISABLED shipped by game Data", code: "MASTER_DISABLED", plugin: "Child.esp",
			setup: func(f *fixture) {
				base(f)
				f.dataPlugin("Parent.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
				f.modPlugin("M", "Child.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Parent.esp"}, 3))
				f.modlist("+M")
				f.plugins("*Skyrim.esm", "*Child.esp")
			},
			act: func(f *fixture) { f.plugins("*Skyrim.esm", "*Parent.esp", "*Child.esp") },
		},
		{
			name: "MASTER_UNCHECKED_BROKEN", code: "MASTER_UNCHECKED_BROKEN", plugin: "Child.esp",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("Base", "Parent.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Gone.esp"}, 3))
				f.modPlugin("M", "Child.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Parent.esp"}, 3))
				f.modlist("+Base", "+M")
				f.plugins("*Skyrim.esm", "Parent.esp", "*Child.esp")
			},
			act: func(f *fixture) { f.plugins("*Skyrim.esm", "Parent.esp", "Child.esp") },
		},
		{
			name: "MASTER_OUT_OF_ORDER", code: "MASTER_OUT_OF_ORDER", plugin: "Child.esp",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("Base", "Parent.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
				f.modPlugin("M", "Child.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Parent.esp"}, 3))
				f.modlist("+Base", "+M")
				f.plugins("*Skyrim.esm", "*Child.esp", "*Parent.esp")
			},
			act: func(f *fixture) { f.plugins("*Skyrim.esm", "*Parent.esp", "*Child.esp") },
		},
		{
			name: "PLUGIN_MISSING", code: "PLUGIN_MISSING", plugin: "Ghost.esp",
			setup: func(f *fixture) {
				base(f)
				f.plugins("*Skyrim.esm", "*Ghost.esp")
			},
			act: func(f *fixture) { f.plugins("*Skyrim.esm", "Ghost.esp") },
		},
		{
			name: "PLUGIN_IN_DISABLED_MOD", code: "PLUGIN_IN_DISABLED_MOD", plugin: "Solo.esp",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("Base", "Solo.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
				f.modlist("-Base")
				f.plugins("*Skyrim.esm", "*Solo.esp")
			},
			act: func(f *fixture) { f.modlist("+Base") },
		},
		{
			name: "PLUGIN_UNREADABLE", code: "PLUGIN_UNREADABLE", plugin: "Junk.esp",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("M", "Junk.esp", []byte("not a TES4 plugin"))
				f.modlist("+M")
				f.plugins("*Skyrim.esm", "*Junk.esp")
			},
			act: func(f *fixture) { f.plugins("*Skyrim.esm", "Junk.esp") },
		},
		{
			name: "DUPLICATE_ENTRY in plugins.txt", code: "DUPLICATE_ENTRY", plugin: "Shared.esp",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("M", "Shared.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
				f.modlist("+M")
				f.plugins("*Skyrim.esm", "*Shared.esp", "*Shared.esp")
			},
			act: func(f *fixture) { f.plugins("*Skyrim.esm", "*Shared.esp") },
		},
		// The repeat can sit in loadorder.txt instead, with plugins.txt naming the
		// plugin once: the enabled order is what the engine reads, so that is the
		// file whose duplicate has to be found and whose edit has to clear it.
		{
			name: "DUPLICATE_ENTRY in loadorder.txt", code: "DUPLICATE_ENTRY", plugin: "Shared.esp",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("M", "Shared.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
				f.modlist("+M")
				f.loadOrder("Skyrim.esm", "Shared.esp", "Shared.esp")
				f.plugins("*Skyrim.esm", "*Shared.esp")
			},
			act: func(f *fixture) { f.loadOrder("Skyrim.esm", "Shared.esp") },
		},
		{
			name: "NOT_IN_LOADORDER", code: "NOT_IN_LOADORDER", plugin: "Loose.esp",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("M", "Loose.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
				f.modlist("+M")
				f.write("profiles/Default/loadorder.txt", "Skyrim.esm\n")
				f.plugins("*Skyrim.esm", "*Loose.esp")
			},
			act: func(f *fixture) { f.write("profiles/Default/loadorder.txt", "Skyrim.esm\nLoose.esp\n") },
		},
		{
			name: "NO_MODLIST", code: "NO_MODLIST", plugin: "",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("Keep", "Keep.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
				f.plugins("*Skyrim.esm", "*Keep.esp")
			},
			act: func(f *fixture) { f.modlist("+Keep") },
		},
		{
			name: "TOO_MANY_PLUGINS", code: "TOO_MANY_PLUGINS", plugin: "",
			setup: func(f *fixture) {
				base(f)
				lines := []string{"*Skyrim.esm"}
				for i := 0; i < 254; i++ {
					name := fmt.Sprintf("P%03d.esp", i)
					f.modPlugin("M", name, buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 0))
					lines = append(lines, "*"+name)
				}
				f.modlist("+M")
				f.plugins(lines...)
			},
			act: func(f *fixture) {
				// Compact one small plugin to an ESL, as the advice says.
				f.modPlugin("M", "P000.esp", buildPlugin(flagLight, 1.7, []string{"Skyrim.esm"}, 0))
			},
		},
		{
			name: "NEAR_PLUGIN_LIMIT", code: "NEAR_PLUGIN_LIMIT", plugin: "",
			setup: func(f *fixture) {
				base(f)
				lines := []string{"*Skyrim.esm"}
				for i := 0; i < 249; i++ {
					name := fmt.Sprintf("P%03d.esp", i)
					f.modPlugin("M", name, buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 0))
					lines = append(lines, "*"+name)
				}
				f.modlist("+M")
				f.plugins(lines...)
			},
			act: func(f *fixture) {
				f.modPlugin("M", "P000.esp", buildPlugin(flagLight, 1.7, []string{"Skyrim.esm"}, 0))
			},
		},
		{
			name: "TOO_MANY_LIGHT_PLUGINS", code: "TOO_MANY_LIGHT_PLUGINS", plugin: "", opts: Options{LightPluginLimit: 3},
			setup: func(f *fixture) {
				base(f)
				lines := []string{"*Skyrim.esm"}
				for i := 0; i < 4; i++ {
					name := fmt.Sprintf("L%04d.esl", i)
					f.modPlugin("M", name, buildPlugin(flagLight, 1.7, []string{"Skyrim.esm"}, 0))
					lines = append(lines, "*"+name)
				}
				f.modlist("+M")
				f.plugins(lines...)
			},
			act: func(f *fixture) {
				// Remove one light plugin, as the advice allows.
				f.plugins("*Skyrim.esm", "*L0000.esl", "*L0001.esl", "*L0002.esl")
			},
		},
		{
			name: "LIGHT_OVER_CAPACITY", code: "LIGHT_OVER_CAPACITY", plugin: "Big.esl", opts: Options{EslRecordLimit: 10},
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("M", "Big.esl", buildPlugin(flagLight, 1.7, []string{"Skyrim.esm"}, 11))
				f.modlist("+M")
				f.plugins("*Skyrim.esm", "*Big.esl")
			},
			act: func(f *fixture) {
				// The other remedy: clear the ESL flag and take a full slot.
				f.modPlugin("M", "Big.esl", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 11))
			},
		},
		{
			name: "LIGHT_NEAR_CAPACITY", code: "LIGHT_NEAR_CAPACITY", plugin: "Big.esl", opts: Options{EslRecordLimit: 10},
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("M", "Big.esl", buildPlugin(flagLight, 1.7, []string{"Skyrim.esm"}, 10))
				f.modlist("+M")
				f.plugins("*Skyrim.esm", "*Big.esl")
			},
			act: func(f *fixture) {
				// Compact it, which is what the advice asks for.
				f.modPlugin("M", "Big.esl", buildPlugin(flagLight, 1.7, []string{"Skyrim.esm"}, 5))
			},
		},
		{
			name: "MASTER_BROKEN", code: "MASTER_BROKEN", plugin: "Dependent.esp",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("M", "Needy.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Gone.esp"}, 3))
				f.modPlugin("M", "Dependent.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Needy.esp"}, 3))
				f.modlist("+M")
				f.plugins("*Skyrim.esm", "*Needy.esp", "*Dependent.esp")
			},
			act: func(f *fixture) { f.plugins("*Skyrim.esm", "*Needy.esp", "Dependent.esp") },
		},
		{
			name: "SAVE_PLUGIN_MISSING", code: "SAVE_PLUGIN_MISSING", plugin: "Saved.esp",
			setup: func(f *fixture) {
				base(f)
				f.modPlugin("Keep", "Saved.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
				f.modlist("+Keep")
				f.plugins("*Skyrim.esm")
				f.save("Save1.ess", "Skyrim.esm", "Saved.esp")
			},
			act: func(f *fixture) { f.plugins("*Skyrim.esm", "*Saved.esp") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.setup(f)
			// Every row looks only at the profile's own saves folder, so a save in
			// the developer's Documents folder cannot change the result.
			run := func() *Result { return f.runWithSaves(tc.opts) }

			if findFinding(run(), tc.code, tc.plugin) == nil {
				t.Fatalf("fixture does not produce %s for %q", tc.code, tc.plugin)
			}
			tc.act(f)
			if fd := findFinding(run(), tc.code, tc.plugin); fd != nil {
				t.Errorf("%s for %q is still reported after its advice was followed: %+v", tc.code, tc.plugin, fd)
			}
		})
	}
}
