package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Engine limits.
const (
	maxFullPlugins  = 254
	warnFullPlugins = 250
	maxLightPlugins = 4096

	// defaultEslRecordLimit is the FormID budget of a light plugin on Skyrim
	// 1.6.1130 and later. Builds before that allowed 2048. The limit is a
	// property of the game, not of the plugin's own header version, which is
	// why official Creation Club ESLs with 1.70 headers can exceed 2048.
	defaultEslRecordLimit = 4096
)

// Options tunes audit behaviour.
//
// EslRecordLimit is how many records a light plugin may hold on the installed
// game: 4096 for Skyrim 1.6.1130+, 2048 for earlier builds (pass -esl-limit).
type Options struct {
	EslRecordLimit int
	// LightPluginLimit overrides the engine's light-plugin ceiling. Zero uses
	// maxLightPlugins; tests lower it so the ceiling is reachable with a few
	// files instead of thousands.
	LightPluginLimit int
	// Game is the installed Skyrim build. When EslRecordLimit is 0 its version
	// decides the light-plugin cap, and it is reported so the user can pick mod
	// files built for the right executable.
	Game *GameVersion
	// SaveDirs overrides where saves are looked for. Empty means the profile's
	// own saves folder and the game's Documents folder, in that order; tests use
	// it so they never read the real machine's saves.
	SaveDirs []string
}

// Severity of a finding.
type Severity string

const (
	SevError Severity = "error"
	SevWarn  Severity = "warn"
	SevInfo  Severity = "info"
)

// Finding is one problem found in the load order.
type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Plugin   string   `json:"plugin,omitempty"`
	Mod      string   `json:"mod,omitempty"`
	Winner   string   `json:"winner,omitempty"`
	Files    int      `json:"files,omitempty"`
	Title    string   `json:"title,omitempty"`
	Detail   string   `json:"detail"`
	Fix      string   `json:"fix,omitempty"`
	Search   string   `json:"search,omitempty"`
}

// Counts summarises findings by severity.
type Counts struct {
	Error int `json:"error"`
	Warn  int `json:"warn"`
	Info  int `json:"info"`
}

// Result is the outcome of an audit.
type Result struct {
	Instance         string        `json:"instance"`
	Profile          string        `json:"profile"`
	PluginsTxt       string        `json:"plugins_txt"`
	GameData         string        `json:"game_data,omitempty"`
	Listed           int           `json:"listed"`
	Enabled          int           `json:"enabled"`
	Full             int           `json:"full_plugins"`
	Light            int           `json:"light_plugins"`
	EslLimit         int           `json:"esl_record_limit"`
	GameVersion      string        `json:"game_version,omitempty"`
	GameEdition      string        `json:"game_edition,omitempty"`
	GameExe          string        `json:"game_exe,omitempty"`
	GameNote         string        `json:"game_note,omitempty"`
	Counts           Counts        `json:"counts"`
	Findings         []Finding     `json:"findings"`
	Mods             []ModSummary  `json:"mods,omitempty"`
	ExpectedOverlaps []OverlapNote `json:"expected_overlaps,omitempty"`
	// BrokenPlugins are the enabled plugins that cannot load, directly or
	// through a master. The UI offers to uncheck exactly this set.
	BrokenPlugins []string `json:"broken_plugins,omitempty"`
	// DisableCandidates is the subset of BrokenPlugins that cannot be repaired by
	// enabling something: a master they need is installed nowhere. Only these may
	// be offered for unchecking; the rest load as soon as their master is enabled.
	DisableCandidates []string `json:"disable_candidates,omitempty"`
	// SaveFile is the newest save that was read for the dependency check, and
	// SavePlugins the number of plugins it was made with. Empty when no save was
	// found, which is not a problem: the audit just has nothing to compare.
	SaveFile    string `json:"save_file,omitempty"`
	SavePlugins int    `json:"save_plugins,omitempty"`
}

// OverlapNote is a file override that is by design, because the higher-priority
// mod requires the lower one.
type OverlapNote struct {
	Winner string `json:"winner"`
	Loser  string `json:"loser"`
	Files  int    `json:"files"`
	Reason string `json:"reason,omitempty"`
}

type findingGuidance struct{ title, fix string }

// findingGuide turns a machine code into what a player needs to read: a plain
// title and the concrete remedy.
var findingGuide = map[string]findingGuidance{
	"MASTER_MISSING":          {"Missing master file", "Install the mod that ships this master, or disable the plugin that needs it. The game stops at the main menu while a master is missing."},
	"MASTER_DISABLED":         {"Master not enabled", "Switch the master on in MO2's right pane (or plugins.txt) and keep it above the plugin that requires it."},
	"MASTER_UNCHECKED":        {"Master switched off in an enabled mod", "Tick the master in MO2's right pane and keep it above this plugin; its mod is already enabled, so toggling the mod cannot help."},
	"MASTER_UNCHECKED_BROKEN": {"Master switched off and unloadable", "Ticking the master cannot work while it is broken. Repair what it is missing, or disable this plugin."},
	"MASTER_OUT_OF_ORDER":     {"Master loads too late", "Move this plugin below its master in the load order, or re-sort with LOOT."},
	"PLUGIN_MISSING":          {"Plugin file missing", "The plugin is enabled but no installed mod ships the file. Reinstall the mod that owns it, or untick the plugin."},
	"PLUGIN_IN_DISABLED_MOD":  {"Plugin lives in a disabled mod", "Enable the mod that ships this plugin, or untick the plugin."},
	"PLUGIN_UNREADABLE":       {"Plugin cannot be read", "The file is corrupt or not a TES4 plugin. Reinstall the mod that ships it, or untick the plugin."},
	"DUPLICATE_PLUGIN":        {"Duplicate plugin file", "More than one enabled mod ships this plugin; the higher-priority copy wins. Delete the copy in the losing mod, or disable that mod, so updates cannot fight over it."},
	"DUPLICATE_ENTRY":         {"Duplicate load-order entry", "Remove the repeated line from plugins.txt and loadorder.txt."},
	"NOT_IN_LOADORDER":        {"Not in the load order", "Re-save the load order in MO2, or add the plugin to loadorder.txt."},
	"TOO_MANY_PLUGINS":        {"Over the full-plugin ceiling", "Merge small plugins or compact ESL-able ones in xEdit until fewer than 254 full plugins remain."},
	"TOO_MANY_LIGHT_PLUGINS":  {"Over the light-plugin ceiling", "Merge or remove light plugins; the engine stops loading them past 4096."},
	"NEAR_PLUGIN_LIMIT":       {"Close to the plugin ceiling", "Free full-plugin slots by merging or ESL-flagging small plugins before adding more."},
	"LIGHT_OVER_CAPACITY":     {"Light plugin over capacity", "Compact the plugin in xEdit (Compact FormIDs for ESL), or clear the ESL flag and take a full slot."},
	"LIGHT_NEAR_CAPACITY":     {"Light plugin nearly full", "Compact this plugin before adding more records to it."},
	"NO_MODLIST":              {"No mod priority known", "Let MO2 write modlist.txt again by opening this profile and closing it, or restore the file from a backup; without it, duplicate plugin files cannot be priority-resolved."},
	"OVERLAP":                 {"Loose-file override", "Nothing needs installing: the higher-priority mod simply wins these files. Only act if something looks wrong in game."},
	"MASTER_BROKEN":           {"Depends on a broken plugin", "This plugin masters one that cannot load, so the engine stops here too. Disable this plugin, or repair the master it needs."},
	"SAVE_PLUGIN_MISSING":     {"Save needs a plugin that is not loaded", "This save references the plugin, so loading it now crashes or drops objects. Re-enable the plugin that ships it, or go back to a save made without it; new games are unaffected."},
}

func (r *Result) add(sev Severity, code, plugin, detail string) {
	r.addFor(sev, code, plugin, "", detail)
}

// addFor records a finding that also names the mod responsible, so the UI can
// offer to quarantine or delete it.
func (r *Result) addFor(sev Severity, code, plugin, mod, detail string) {
	f := Finding{Severity: sev, Code: code, Plugin: plugin, Mod: mod, Detail: detail}
	if g, ok := findingGuide[code]; ok {
		f.Title, f.Fix = g.title, g.fix
	}
	if f.Title == "" {
		f.Title = titleFromCode(code)
	}
	if plugin != "" && searchableCodes[code] {
		f.Search = strings.TrimSuffix(plugin, filepath.Ext(plugin))
	}
	r.Findings = append(r.Findings, f)
	switch sev {
	case SevError:
		r.Counts.Error++
	case SevWarn:
		r.Counts.Warn++
	default:
		r.Counts.Info++
	}
}

// searchableCodes are the findings a Nexus lookup can actually help with: the
// file the plugin needs was never installed. Offering mod search for capacity or
// override findings would only suggest downloads that change nothing.
var searchableCodes = map[string]bool{
	"MASTER_MISSING":      true,
	"PLUGIN_MISSING":      true,
	"SAVE_PLUGIN_MISSING": true,
}

type pluginRef struct {
	mod     string
	path    string
	enabled bool
}

// Audit checks a profile's load order: plugin presence, master availability and
// ordering, light-plugin FormID capacity, and the engine's plugin ceilings.
func Audit(m *Mo2Instance, opts Options) (*Result, error) {
	entries, err := ReadPluginsTxt(m.PluginsTxt)
	if err != nil {
		return nil, fmt.Errorf("cannot read plugins.txt: %w", err)
	}

	// modlist.txt is optional: without it, duplicate files cannot be priority
	// resolved, but existence checks still work.
	mods, modsErr := ReadModList(m.ModListTxt)

	eslLimit := opts.EslRecordLimit
	if eslLimit <= 0 && opts.Game != nil {
		eslLimit = opts.Game.EslLimit
	}
	if eslLimit <= 0 {
		eslLimit = defaultEslRecordLimit
	}
	lightLimit := opts.LightPluginLimit
	if lightLimit <= 0 {
		lightLimit = maxLightPlugins
	}

	res := &Result{
		Instance:   m.Dir,
		Profile:    m.Profile,
		PluginsTxt: m.PluginsTxt,
		GameData:   m.GameData,
		Listed:     len(entries),
		EslLimit:   eslLimit,
	}
	if opts.Game != nil {
		res.GameVersion = opts.Game.Version
		res.GameEdition = opts.Game.Edition
		res.GameExe = opts.Game.Path
		res.GameNote = opts.Game.Note
	}

	refs := indexPlugins(m.ModsDir, mods)
	pluginCount := map[string]int{}
	modPlugins := map[string][]string{}
	pluginName := map[string]string{}
	for plugin, list := range refs {
		for _, r := range list {
			pluginCount[r.mod]++
			modPlugins[r.mod] = append(modPlugins[r.mod], plugin)
			if pluginName[plugin] == "" {
				pluginName[plugin] = filepath.Base(r.path)
			}
		}
	}

	// Which mod ships each plugin at the priority MO2 loads, and what each
	// enabled plugin masters: together these tell a designed override (an addon
	// requiring its parent mod) apart from an accidental one.
	pluginOwner := map[string]string{}
	masters := map[string][]string{}
	// Why a switched-off plugin would still fail to load, keyed by lowercased
	// name: several dependents can name the same master, and its header only
	// has to be read once.
	unloadable := map[string]string{}

	// loadorder.txt carries the full ordered list, base-game masters and Creation
	// Club content included, while plugins.txt carries enabled state for the
	// plugins MO2 manages. A plugin in loadorder.txt but absent from plugins.txt
	// is not tracked by MO2 and is loaded by the engine, so it is part of the
	// order. Auditing plugins.txt alone would report every master as missing.
	order, _ := ReadLoadOrder(m.LoadOrderTxt)
	orderSet := map[string]bool{}
	for _, name := range order {
		orderSet[strings.ToLower(name)] = true
	}

	enabledState := map[string]PluginEntry{}
	for _, e := range entries {
		enabledState[strings.ToLower(e.Name)] = e
	}

	var enabled []string
	enabledIdx := map[string]int{}
	addEnabled := func(name string) {
		key := strings.ToLower(name)
		if prev, dup := enabledIdx[key]; dup {
			res.add(SevWarn, "DUPLICATE_ENTRY", name,
				fmt.Sprintf("appears twice in the load order (positions %d and %d); the later entry is used", prev+1, len(enabled)+1))
			return
		}
		enabledIdx[key] = len(enabled)
		enabled = append(enabled, name)
	}

	if len(order) > 0 {
		res.Listed = len(order)
	}
	for _, name := range order {
		if e, tracked := enabledState[strings.ToLower(name)]; tracked {
			if e.Enabled {
				addEnabled(name)
			}
			continue
		}
		addEnabled(name)
	}
	for _, e := range entries {
		if !e.Enabled || orderSet[strings.ToLower(e.Name)] {
			continue
		}
		if len(order) > 0 {
			res.add(SevWarn, "NOT_IN_LOADORDER", e.Name,
				"enabled in plugins.txt but absent from loadorder.txt; MO2 will not load it")
		}
		addEnabled(e.Name)
	}
	res.Enabled = len(enabled)
	if modsErr != nil {
		res.add(SevInfo, "NO_MODLIST", "",
			"modlist.txt not found, so duplicate plugin files cannot be priority-resolved")
	}

	broken := map[string]bool{}
	unfixable := map[string]bool{}
	for i, name := range enabled {
		path, source, shippedBy := m.locate(refs, name)
		// locate falls back to a disabled mod's copy so master checks can tell
		// "exists but unchecked" from "gone". For the plugin itself that fallback
		// does not load, so the file has to be reachable at its chosen path.
		active := path != "" && (source == "Data" || m.shippedByEnabled(refs, name, path))
		if !active {
			broken[strings.ToLower(name)] = true
			if len(shippedBy) == 0 {
				// Nothing on disk ships it: unchecking is the only remedy.
				unfixable[strings.ToLower(name)] = true
				res.add(SevError, "PLUGIN_MISSING", name,
					"enabled in plugins.txt but the file is in no mod folder and not in game Data")
			} else {
				res.add(SevError, "PLUGIN_IN_DISABLED_MOD", name,
					fmt.Sprintf("enabled in plugins.txt but only shipped by disabled mod(s): %s", strings.Join(shippedBy, ", ")))
			}
			continue
		}
		if loaders := enabledShippers(refs[strings.ToLower(name)]); len(loaders) > 1 {
			res.add(SevWarn, "DUPLICATE_PLUGIN", name,
				fmt.Sprintf("same filename shipped by %d enabled mods (%s); %s wins", len(loaders), strings.Join(loaders, ", "), source))
		}

		plugin, err := ReadPluginHeader(path)
		if err != nil {
			broken[strings.ToLower(name)] = true
			unfixable[strings.ToLower(name)] = true
			res.add(SevError, "PLUGIN_UNREADABLE", name, err.Error())
			continue
		}
		key := strings.ToLower(name)
		masters[key] = plugin.Masters
		if owner, ok := strings.CutPrefix(source, "mod:"); ok {
			pluginOwner[key] = owner
		}
		if plugin.Light {
			res.Light++
		} else {
			res.Full++
		}

		for _, master := range plugin.Masters {
			pos, ok := enabledIdx[strings.ToLower(master)]
			if !ok {
				// Name the mod that already has the master, so the fix is one
				// checkbox instead of a hunt through 100 mod folders.
				broken[strings.ToLower(name)] = true
				masterPath, source, masterMods := m.locate(refs, master)
				loaders := enabledShippers(refs[strings.ToLower(master)])
				switch {
				case masterPath == "":
					// The master is installed nowhere, so this plugin cannot be
					// repaired by enabling anything: it is a disable candidate.
					unfixable[strings.ToLower(name)] = true
					res.add(SevError, "MASTER_MISSING", name,
						fmt.Sprintf("master %q is in no installed mod and not in the game Data folder - the plugin that needs it cannot load", master))
				case len(loaders) > 0:
					// The mod is already enabled, so its checkbox is not the remedy:
					// the master plugin itself is what has to be switched back on,
					// and only if that plugin could load in the first place.
					mkey := strings.ToLower(master)
					why, checked := unloadable[mkey]
					if !checked {
						why = m.cannotLoad(refs, masterPath)
						unloadable[mkey] = why
					}
					if why != "" {
						unfixable[strings.ToLower(name)] = true
						res.add(SevError, "MASTER_UNCHECKED_BROKEN", name,
							fmt.Sprintf("master %q is switched off and cannot load anyway: %s - ticking it will not help, so repair it or disable this plugin", master, why))
					} else {
						res.add(SevError, "MASTER_UNCHECKED", name,
							fmt.Sprintf("master %q is switched off but already installed in an enabled mod (%s); switch the master itself on in MO2's right pane, not the mod", master, strings.Join(loaders, ", ")))
					}
				case source == "Data":
					res.add(SevError, "MASTER_DISABLED", name,
						fmt.Sprintf("master %q is in the game Data folder but not enabled in this load order", master))
				default:
					res.add(SevError, "MASTER_DISABLED", name,
						fmt.Sprintf("master %q is unchecked; it is shipped only by disabled mod(s) %s - enable that mod and keep it above this plugin", master, strings.Join(masterMods, ", ")))
				}
				continue
			}
			if pos > i {
				res.add(SevError, "MASTER_OUT_OF_ORDER", name,
					fmt.Sprintf("master %q loads at position %d, after this plugin at %d", master, pos+1, i+1))
			}
		}

		if plugin.Light {
			switch {
			case plugin.RecordCount > eslLimit:
				res.add(SevError, "LIGHT_OVER_CAPACITY", name,
					fmt.Sprintf("%d records, over the %d-record light-plugin cap (-esl-limit 2048 for pre-1.6.1130 games)", plugin.RecordCount, eslLimit))
			case plugin.RecordCount > eslLimit*9/10:
				res.add(SevInfo, "LIGHT_NEAR_CAPACITY", name,
					fmt.Sprintf("%d of %d light-plugin records used; compact before adding more", plugin.RecordCount, eslLimit))
			}
		}
	}

	if res.Full > maxFullPlugins {
		res.add(SevError, "TOO_MANY_PLUGINS", "",
			fmt.Sprintf("%d full plugins enabled; the engine loads at most %d", res.Full, maxFullPlugins))
	} else if res.Full >= warnFullPlugins {
		res.add(SevWarn, "NEAR_PLUGIN_LIMIT", "",
			fmt.Sprintf("%d full plugins enabled; ceiling is %d", res.Full, maxFullPlugins))
	}
	if res.Light > lightLimit {
		res.add(SevError, "TOO_MANY_LIGHT_PLUGINS", "",
			fmt.Sprintf("%d light plugins enabled; the engine loads at most %d", res.Light, lightLimit))
	}

	// A plugin whose master cannot load cannot load either, and neither can
	// anything mastering it. Reporting only the first link leaves the user with a
	// short fix list and a load order that still stops, so the whole chain is named.
	// Every master is examined on every pass. A plugin can have one master that
	// only needs enabling and another that is installed nowhere; the second one
	// decides whether unchecking is the only local remedy, so stopping at the
	// first broken master would make the answer depend on header order. A plugin
	// already marked broken is revisited for the same reason: its remaining
	// masters may only be classified on a later pass.
	for changed := true; changed; {
		changed = false
		for _, name := range enabled {
			key := strings.ToLower(name)
			for _, master := range masters[key] {
				lowered := strings.ToLower(master)
				if !broken[lowered] {
					continue
				}
				if !broken[key] {
					broken[key] = true
					res.add(SevError, "MASTER_BROKEN", name,
						fmt.Sprintf("master %q cannot load, so this plugin cannot either", master))
					changed = true
				}
				if unfixable[lowered] && !unfixable[key] {
					unfixable[key] = true
					changed = true
				}
			}
		}
	}
	if len(broken) > 0 {
		res.BrokenPlugins = brokenList(entries, broken)
		res.DisableCandidates = brokenList(entries, unfixable)
	}

	// A save remembers the plugins it was made with. One that is no longer loaded
	// is the usual reason a save crashes after a mod was removed, so the newest
	// save is checked when there is one.
	if save := latestSave(m, opts.SaveDirs); save != nil {
		res.SaveFile = save.Path
		res.SavePlugins = len(save.Plugins)
		reportSaveDependencies(res, m, refs, enabledIdx, save)
	}

	// Loose-file conflicts, and the per-mod file counts the UI lists. The walk is
	// bounded, so a huge texture collection cannot stall the audit.
	scan := scanModFiles(m.ModsDir, mods)
	res.Mods = scan.summaries(pluginCount)
	for _, ov := range scan.Overlaps {
		if reason, byDesign := expectedOverride(ov, modPlugins, pluginName, pluginOwner, masters); byDesign {
			res.ExpectedOverlaps = append(res.ExpectedOverlaps, OverlapNote{
				Winner: ov.Winner, Loser: ov.Loser, Files: ov.Files, Reason: reason,
			})
			continue
		}
		res.addOverlap(ov)
	}

	sortFindings(res.Findings)
	return res, nil
}

// brokenList returns the enabled plugins that cannot load, in plugins.txt order,
// so the UI can offer to uncheck exactly that set.
func brokenList(entries []PluginEntry, broken map[string]bool) []string {
	var out []string
	for _, e := range entries {
		if e.Enabled && broken[strings.ToLower(e.Name)] {
			out = append(out, e.Name)
		}
	}
	return out
}

// addOverlap records an override between two unrelated mods. The remedy is
// deliberately not "install something": an override is how mods compose, so the
// only real choices are keeping it or changing which mod owns the files.
func (r *Result) addOverlap(ov modOverlap) {
	detail := fmt.Sprintf("%s overrides %d file(s) from %s", ov.Winner, ov.Files, ov.Loser)
	if len(ov.Examples) > 0 {
		detail += " (e.g. " + strings.Join(ov.Examples, ", ") + ")"
	}
	r.addFor(SevInfo, "OVERLAP", "", ov.Loser, detail)
	f := &r.Findings[len(r.Findings)-1]
	f.Winner = ov.Winner
	f.Files = ov.Files
	f.Fix = fmt.Sprintf("%s loads after %s, so its copies of these files win. That is a normal override and nothing needs installing; only act if the result looks wrong in game, and then reinstall the mod whose files you want or hide the losing ones.", ov.Winner, ov.Loser)
}

// expectedOverride reports whether the winning mod requires the losing one: an
// addon, patch or body replacer that masters the mod whose files it replaces.
// Those overrides are by design and never a problem.
func expectedOverride(ov modOverlap, modPlugins map[string][]string, pluginName, pluginOwner map[string]string, masters map[string][]string) (string, bool) {
	for _, plugin := range modPlugins[ov.Winner] {
		for _, master := range masters[plugin] {
			if pluginOwner[strings.ToLower(master)] == ov.Loser {
				name := pluginName[plugin]
				if name == "" {
					name = plugin
				}
				return fmt.Sprintf("%s masters %s", name, master), true
			}
		}
	}
	return "", false
}

// maxRequirementLookups bounds how many Nexus requirement answers one audit
// fetches, so a long conflict list cannot make the run crawl. Repeats for the same
// mod are answered below and from the client's cache, so they cost no slot.
const maxRequirementLookups = 10

// suppressRequiredOverrides drops overrides that are expected because the
// higher-priority mod lists the lower one as a Nexus requirement: that is how an
// addon or body replacer replaces its parent's files on purpose. It uses the mod
// ids MO2 records in meta.ini and stays quiet when offline or when either mod has
// no page. Both the web UI and the terminal report call it, so they agree on what
// counts as a problem.
func suppressRequiredOverrides(res *Result, nx *NexusClient) {
	if nx == nil || nx.disabled || len(res.Findings) == 0 {
		return
	}
	ids := make(map[string]int, len(res.Mods))
	for _, m := range res.Mods {
		if m.NexusID > 0 {
			ids[m.Name] = m.NexusID
		}
	}
	if len(ids) == 0 {
		return
	}

	lookups := 0
	answers := make(map[int][]NexusRequirement)
	kept := res.Findings[:0]
	for _, f := range res.Findings {
		if f.Code != "OVERLAP" {
			kept = append(kept, f)
			continue
		}
		winnerID, loserID := ids[f.Winner], ids[f.Mod]
		if winnerID == 0 || loserID == 0 {
			kept = append(kept, f)
			continue
		}
		reqs, asked := answers[winnerID]
		if !asked {
			if lookups >= maxRequirementLookups {
				kept = append(kept, f)
				continue
			}
			lookups++
			var err error
			if reqs, err = nx.Requirements(winnerID); err != nil {
				kept = append(kept, f)
				continue
			}
			answers[winnerID] = reqs
		}
		var match *NexusRequirement
		for i := range reqs {
			if reqs[i].ModID == loserID {
				match = &reqs[i]
				break
			}
		}
		if match == nil {
			kept = append(kept, f)
			continue
		}
		res.Counts.Info--
		res.ExpectedOverlaps = append(res.ExpectedOverlaps, OverlapNote{
			Winner: f.Winner,
			Loser:  f.Mod,
			Files:  f.Files,
			Reason: fmt.Sprintf("%s lists %s as a requirement on Nexus", f.Winner, match.Name),
		})
	}
	res.Findings = kept
}

// titleFromCode is the fallback label for a code without guidance text.
func titleFromCode(code string) string {
	words := strings.ToLower(strings.ReplaceAll(code, "_", " "))
	if words == "" {
		return "Finding"
	}
	return strings.ToUpper(words[:1]) + words[1:]
}

// enabledShippers lists the distinct enabled mods shipping a plugin. A disabled
// mod is never mounted, so its copy cannot win or fight: disabling the losing mod
// is one of the warning's own remedies, and it has to clear the warning.
func enabledShippers(list []pluginRef) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range list {
		if !r.enabled || seen[r.mod] {
			continue
		}
		seen[r.mod] = true
		out = append(out, r.mod)
	}
	return out
}

// cannotLoad explains why an installed plugin would still fail to load: its file
// is unreadable, or a master it names is in no mod and not in game Data. The
// main loop only parses enabled plugins, so a switched-off master needs this
// separate header read before a user is told to switch it on.
func (m *Mo2Instance) cannotLoad(refs map[string][]pluginRef, path string) string {
	p, err := ReadPluginHeader(path)
	if err != nil {
		return fmt.Sprintf("its file cannot be read (%v)", err)
	}
	for _, master := range p.Masters {
		if found, _, _ := m.locate(refs, master); found == "" {
			return fmt.Sprintf("its own master %q is installed nowhere", master)
		}
	}
	return ""
}

// shippedByEnabled reports whether the copy at path comes from an enabled mod,
// which is what decides if MO2 would actually provide that file at runtime.
func (m *Mo2Instance) shippedByEnabled(refs map[string][]pluginRef, name, path string) bool {
	for _, r := range refs[strings.ToLower(name)] {
		if r.path == path {
			return r.enabled
		}
	}
	return false
}

// locate finds the plugin file MO2 would load: the highest-priority enabled mod
// that ships it, else any mod, else the game's Data folder.
func (m *Mo2Instance) locate(refs map[string][]pluginRef, name string) (path, source string, shippedBy []string) {
	list := refs[strings.ToLower(name)]
	for _, r := range list {
		shippedBy = append(shippedBy, r.mod)
		if path == "" && r.enabled {
			path, source = r.path, "mod:"+r.mod
		}
	}
	if path == "" && len(list) > 0 {
		path, source = list[0].path, "mod:"+list[0].mod
	}
	if path == "" && m.GameData != "" {
		if candidate := m.dataPluginPath(name); candidate != "" {
			path, source = candidate, "Data"
		}
	}
	return path, source, shippedBy
}

// dataPluginPath finds a plugin in the game's Data folder the way the engine
// does: names compare without regard to case, so a hand-edited plugins.txt naming
// skyrim.esm has to find the Skyrim.esm the game ships. The exact spelling is
// tried first because it is the common case, and only a miss pays for a directory
// listing. Relying on the filesystem alone would make the audit's answer depend on
// the host: the same profile would report a missing plugin on a case-sensitive
// filesystem and audit clean on Windows.
func (m *Mo2Instance) dataPluginPath(name string) string {
	direct := filepath.Join(m.GameData, name)
	if st, err := os.Stat(direct); err == nil && !st.IsDir() {
		return direct
	}
	entries, err := os.ReadDir(m.GameData)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(e.Name(), name) {
			return filepath.Join(m.GameData, e.Name())
		}
	}
	return ""
}

// indexPlugins maps lowercased plugin filenames to the mod folders shipping
// them, in mod priority order. Only the mod root and one level down are walked,
// which covers the usual FOMOD nesting without scanning every asset folder.
func indexPlugins(modsDir string, mods []ModEntry) map[string][]pluginRef {
	idx := map[string][]pluginRef{}
	seen := map[string]bool{}

	order := modOrder(modsDir, mods)

	for _, mod := range order {
		key := strings.ToLower(mod.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		root := filepath.Join(modsDir, mod.Name)
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}
		collectPlugins(root, mod.Name, mod.Enabled, 2, idx)
	}
	return idx
}

func collectPlugins(dir, modName string, enabled bool, depth int, idx map[string][]pluginRef) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if e.Name() == ".git" {
				continue
			}
			if depth > 1 {
				collectPlugins(full, modName, enabled, depth-1, idx)
			}
			continue
		}
		if !isPluginFile(e.Name()) {
			continue
		}
		key := strings.ToLower(e.Name())
		idx[key] = append(idx[key], pluginRef{mod: modName, path: full, enabled: enabled})
	}
}

func sortFindings(fs []Finding) {
	rank := map[Severity]int{SevError: 0, SevWarn: 1, SevInfo: 2}
	sort.SliceStable(fs, func(i, j int) bool {
		if rank[fs[i].Severity] != rank[fs[j].Severity] {
			return rank[fs[i].Severity] < rank[fs[j].Severity]
		}
		if fs[i].Code != fs[j].Code {
			return fs[i].Code < fs[j].Code
		}
		return fs[i].Plugin < fs[j].Plugin
	})
}

// Render produces the terminal report.
func (r *Result) Render(verbose bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "skyrim-audit - Mod Organizer 2 profile audit\n")
	fmt.Fprintf(&b, "  instance: %s\n", r.Instance)
	fmt.Fprintf(&b, "  profile:  %s\n", r.Profile)
	if r.GameData != "" {
		fmt.Fprintf(&b, "  data:     %s\n", r.GameData)
	} else {
		fmt.Fprintf(&b, "  data:     (unknown - pass -data for master checks against the game)\n")
	}
	if r.GameVersion != "" {
		fmt.Fprintf(&b, "  game:     Skyrim %s (%s) - light plugins hold up to %d records\n",
			r.GameVersion, r.GameEdition, r.EslLimit)
	} else {
		fmt.Fprintf(&b, "  game:     version not detected - light plugins assumed to hold %d records\n", r.EslLimit)
	}
	fmt.Fprintf(&b, "  plugins:  %d listed, %d enabled (%d full, %d light)\n",
		r.Listed, r.Enabled, r.Full, r.Light)

	sections := []struct {
		sev   Severity
		title string
	}{
		{SevError, "ERRORS"},
		{SevWarn, "WARNINGS"},
		{SevInfo, "INFO"},
	}
	for _, s := range sections {
		if s.sev == SevInfo && !verbose {
			continue
		}
		var items []Finding
		for _, f := range r.Findings {
			if f.Severity == s.sev {
				items = append(items, f)
			}
		}
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s (%d)\n", s.title, len(items))
		for _, f := range items {
			target := f.Plugin
			if target == "" {
				target = "load order"
			}
			fmt.Fprintf(&b, "  %-24s %-28s %s\n", f.Code, target, f.Detail)
		}
	}

	if verbose && len(r.ExpectedOverlaps) > 0 {
		fmt.Fprintf(&b, "\nnote: %d file override(s) not listed because one mod requires the other (e.g. %s over %s)\n",
			len(r.ExpectedOverlaps), r.ExpectedOverlaps[0].Winner, r.ExpectedOverlaps[0].Loser)
	}

	b.WriteString("\n")
	switch {
	case r.Counts.Error > 0:
		fmt.Fprintf(&b, "result: %d error(s), %d warning(s) - this load order will not load as-is\n", r.Counts.Error, r.Counts.Warn)
	case r.Counts.Warn > 0:
		fmt.Fprintf(&b, "result: no errors, %d warning(s)\n", r.Counts.Warn)
	default:
		b.WriteString("result: clean - no missing masters, ordering problems, or capacity issues\n")
	}
	return b.String()
}
