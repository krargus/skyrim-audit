package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The loose-file scan is bounded so an enormous texture collection cannot stall
// an audit. Files past the budget are not counted, in the overlap pairs or in
// the per-mod count, but every mod still gets a row.
const (
	maxFilesPerMod  = 60000
	maxFilesTotal   = 900000
	maxOverlapPairs = 30
)

// ModSummary is one installed mod as the UI lists it.
type ModSummary struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"` // 1 = highest priority, wins file conflicts
	Plugins  int    `json:"plugins"`
	Files    int    `json:"files"`
	NexusID  int    `json:"nexus_id,omitempty"`
	// Thumbnail is the Nexus preview image, filled in by the web UI.
	Thumbnail string `json:"thumbnail,omitempty"`
}

// fileScan is what the loose-file walk found: the number of content files each
// mod ships, and the overrides between mods. It keeps counts rather than path
// sets because nothing needs a mod's file list once the walk is done.
type fileScan struct {
	Counts   map[string]int
	Order    []string
	Enabled  map[string]bool
	Meta     map[string]modMeta
	Overlaps []modOverlap
}

// overlapStat accumulates one winner/loser pair while the walk runs.
type overlapStat struct {
	Files    int
	Examples []string
}

// modOverlap is one mod overriding loose files shipped by another.
type modOverlap struct {
	Winner   string
	Loser    string
	Files    int
	Examples []string
}

// modOrder returns the mods in MO2 priority order, highest first. Without a
// modlist.txt every folder in mods/ is treated as enabled.
func modOrder(modsDir string, mods []ModEntry) []ModEntry {
	if len(mods) > 0 {
		return mods
	}
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		return nil
	}
	var order []ModEntry
	for _, e := range entries {
		if e.IsDir() {
			order = append(order, ModEntry{Name: e.Name(), Enabled: true})
		}
	}
	return order
}

// scanLimits bounds one walk. It is a parameter rather than two constants so a
// test can exhaust the budget with a handful of files.
type scanLimits struct {
	PerMod int
	Total  int
}

var defaultScanLimits = scanLimits{PerMod: maxFilesPerMod, Total: maxFilesTotal}

// scanModFiles walks every mod once, in priority order, counting content files
// and recording which mod wins each path. One owner entry per path replaces both
// the per-mod path sets and the separate join pass that used to pair them, so
// memory no longer grows with the number of copies of a file.
func scanModFiles(modsDir string, mods []ModEntry) fileScan {
	return scanModFilesWithLimits(modsDir, mods, defaultScanLimits)
}

func scanModFilesWithLimits(modsDir string, mods []ModEntry, limits scanLimits) fileScan {
	scan := fileScan{
		Counts:  map[string]int{},
		Enabled: map[string]bool{},
		Meta:    map[string]modMeta{},
	}
	// One entry per path: the first enabled mod to ship it owns it, and every
	// later copy counts as a conflict with that owner. Per-pair stats share a
	// single map, so the whole collection costs one owner entry per unique path
	// and nothing per copy.
	owners := map[string]string{}
	pairs := map[[2]string]*overlapStat{}
	seen := map[string]bool{}
	budget := limits.Total
	for _, mod := range modOrder(modsDir, mods) {
		name := mod.Name
		if seen[name] {
			continue
		}
		seen[name] = true
		root := filepath.Join(modsDir, name)
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}
		scan.Order = append(scan.Order, name)
		scan.Enabled[name] = mod.Enabled
		scan.Meta[name] = readModMeta(root)
		limit := limits.PerMod
		if budget < limit {
			limit = budget
		}
		left := limit
		walkModFiles(root, "", 0, &left, func(file string) {
			// A disabled mod's files are counted for its table row but cannot
			// conflict: MO2 never mounts them.
			if !mod.Enabled {
				return
			}
			owner, taken := owners[file]
			if !taken {
				owners[file] = name
				return
			}
			key := [2]string{owner, name}
			stat := pairs[key]
			if stat == nil {
				stat = &overlapStat{}
				pairs[key] = stat
			}
			stat.Files++
			if len(stat.Examples) < 3 {
				stat.Examples = append(stat.Examples, file)
			}
		})
		used := limit - left
		budget -= used
		scan.Counts[name] = used
	}
	scan.Overlaps = overlapPairs(pairs)
	return scan
}

// walkModFiles visits every content file under dir, passing its lowercase path
// relative to the mod root. Documentation and plugins are skipped: they are
// noise here and plugin duplication is reported separately.
func walkModFiles(dir, prefix string, depth int, budget *int, visit func(string)) {
	if depth > 8 || *budget <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if *budget <= 0 {
			return
		}
		name := e.Name()
		rel := name
		if prefix != "" {
			rel = prefix + "/" + name
		}
		if e.IsDir() {
			switch strings.ToLower(name) {
			case ".git", "fomod", "docs":
				continue
			}
			walkModFiles(filepath.Join(dir, name), rel, depth+1, budget, visit)
			continue
		}
		if isDocFile(name) {
			continue
		}
		if isPluginFile(name) {
			continue
		}
		*budget = *budget - 1
		visit(strings.ToLower(rel))
	}
}

// docFileStems are licence, readme and version files. Almost every mod ships its
// own copy, so they overlap constantly without changing how the game looks or
// runs; counting them would bury the asset conflicts that matter.
var docFileStems = []string{
	"readme", "read me", "licence", "license", "copying", "copyright",
	"changelog", "change log", "changes", "version", "versions", "credits",
	"install", "installation", "uninstall", "permissions", "terms", "notice",
	"disclaimer", "description", "manifest",
}

// docExtensions are formats that only ever hold documentation.
var docExtensions = map[string]bool{
	".md": true, ".pdf": true, ".doc": true, ".docx": true, ".rtf": true,
	".odt": true, ".chm": true, ".htm": true, ".html": true,
}

// junkFiles are host artefacts and mod metadata, not mod content.
var junkFiles = map[string]bool{
	".ds_store": true, "thumbs.db": true, "desktop.ini": true,
	".gitignore": true, ".gitattributes": true, ".gitkeep": true,
	"meta.ini": true,
}

// isDocFile reports whether a file is documentation rather than game content,
// matching both "license" and "README - Skyrim.txt" style names.
func isDocFile(name string) bool {
	lower := strings.ToLower(name)
	if junkFiles[lower] {
		return true
	}
	if docExtensions[filepath.Ext(lower)] {
		return true
	}
	stem := strings.TrimSpace(strings.TrimSuffix(lower, filepath.Ext(lower)))
	for _, prefix := range docFileStems {
		if stem == prefix || strings.HasPrefix(stem, prefix+" ") ||
			strings.HasPrefix(stem, prefix+"_") || strings.HasPrefix(stem, prefix+"-") ||
			strings.HasPrefix(stem, prefix+".") {
			return true
		}
	}
	// "GameVersion.txt", "VersionInfo" and friends.
	return strings.HasSuffix(stem, "version") || strings.HasSuffix(stem, "versions")
}

// overlapPairs turns the accumulated pairs into findings, most overridden first.
func overlapPairs(pairs map[[2]string]*overlapStat) []modOverlap {
	out := make([]modOverlap, 0, len(pairs))
	for key, stat := range pairs {
		out = append(out, modOverlap{Winner: key[0], Loser: key[1], Files: stat.Files, Examples: stat.Examples})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		if out[i].Winner != out[j].Winner {
			return out[i].Winner < out[j].Winner
		}
		return out[i].Loser < out[j].Loser
	})
	if len(out) > maxOverlapPairs {
		out = out[:maxOverlapPairs]
	}
	return out
}

// summaries joins the file scan with the plugin inventory for the UI table.
func (s fileScan) summaries(pluginCount map[string]int) []ModSummary {
	out := make([]ModSummary, 0, len(s.Order))
	for i, name := range s.Order {
		out = append(out, ModSummary{
			Name:     name,
			Enabled:  s.Enabled[name],
			Priority: i + 1,
			Plugins:  pluginCount[name],
			Files:    s.Counts[name],
			NexusID:  s.Meta[name].NexusID,
		})
	}
	return out
}
