package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RemovalRecord is one quarantined mod, kept so it can be put back: where the
// folder went and which plugins stopped being loaded because of it.
type RemovalRecord struct {
	Mod              string    `json:"mod"`
	NewPath          string    `json:"new_path"`
	UncheckedPlugins []string  `json:"unchecked_plugins,omitempty"`
	At               time.Time `json:"at"`
}

// journalDirName keeps the undo journal out of the way of MO2's own files.
const journalDirName = ".skyrim-audit"

func journalPath(m *Mo2Instance) string {
	return filepath.Join(m.Dir, journalDirName, "removals.json")
}

func readJournal(m *Mo2Instance) []RemovalRecord {
	raw, err := os.ReadFile(journalPath(m))
	if err != nil {
		return nil
	}
	var recs []RemovalRecord
	if json.Unmarshal(raw, &recs) != nil {
		return nil
	}
	return recs
}

func writeJournal(m *Mo2Instance, recs []RemovalRecord) error {
	p := journalPath(m)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(raw, '\n'), 0o644)
}

// recordRemoval remembers a quarantine so it can be undone. Best effort: the
// folder has already moved, and a missing journal must not fail the removal.
func recordRemoval(m *Mo2Instance, out *Removal) {
	if out == nil || out.Mode != "quarantine" || out.NewPath == "" {
		return
	}
	recs := readJournal(m)
	recs = append([]RemovalRecord{{
		Mod:              out.Mod,
		NewPath:          out.NewPath,
		UncheckedPlugins: out.UncheckedPlugin,
		At:               time.Now(),
	}}, recs...)
	_ = writeJournal(m, recs)
}

// ListRemovals returns the quarantined mods still on disk, newest first. A folder
// the user deleted by hand simply drops out of the list.
func ListRemovals(m *Mo2Instance) []RemovalRecord {
	var out []RemovalRecord
	for _, r := range readJournal(m) {
		if st, err := os.Stat(r.NewPath); err == nil && st.IsDir() {
			out = append(out, r)
		}
	}
	return out
}

// RestoreMod puts a quarantined mod back, re-enables it in modlist.txt and
// re-checks the plugins it took with it. Plugins go back at the end of the load
// order rather than at their old position; the next audit then reports any
// ordering problem honestly instead of this function guessing.
func RestoreMod(m *Mo2Instance, name string) (*Removal, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("no mod name given")
	}
	recs := readJournal(m)
	idx := -1
	for i, r := range recs {
		if strings.EqualFold(r.Mod, name) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("%q is not in the quarantine journal", name)
	}
	rec := recs[idx]
	// The journal is a file in the instance folder, so treat it as input: the
	// name must be a plain folder name and the source must be one quarantine
	// child, never an arbitrary path.
	if rec.Mod == "" || rec.Mod == "." || rec.Mod == ".." || strings.ContainsAny(rec.Mod, `/\:`) {
		return nil, fmt.Errorf("journal entry %q is not a mod folder name", rec.Mod)
	}
	quarantine := filepath.Join(m.ModsDir, quarantineDirName)
	if rel, err := filepath.Rel(quarantine, rec.NewPath); err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.ContainsAny(rel, `/\`) {
		return nil, fmt.Errorf("journal entry %q is outside the quarantine folder", rec.Mod)
	}
	if st, err := os.Stat(rec.NewPath); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("the quarantined folder is gone: %s", rec.NewPath)
	}
	target := filepath.Join(m.ModsDir, rec.Mod)
	if pathExists(target) {
		return nil, fmt.Errorf("a mod folder named %q already exists - rename or move it first", rec.Mod)
	}
	if err := os.Rename(rec.NewPath, target); err != nil {
		return nil, fmt.Errorf("move %s back: %w", rec.NewPath, err)
	}

	backup, _ := backupFile(m.ModListTxt)
	if err := enableModInModList(m.ModListTxt, rec.Mod); err != nil {
		return nil, err
	}
	plugins, err := enablePlugins(m, rec.UncheckedPlugins)
	if err != nil {
		return nil, err
	}

	recs = append(recs[:idx], recs[idx+1:]...)
	_ = writeJournal(m, recs)

	out := &Removal{Mod: rec.Mod, Mode: "restore", Path: target, NewPath: rec.NewPath, UncheckedPlugin: plugins}
	if backup != "" {
		out.Backups = []string{backup}
	}
	return out, nil
}

// enableModInModList turns "-Mod" back into "+Mod" in place, or puts the line at
// the top (highest priority) when the removal had dropped it.
func enableModInModList(path, name string) error {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read modlist.txt: %w", err)
	}
	text := string(raw)
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	found := false
	for i, line := range lines {
		trim := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if len(trim) < 2 {
			continue
		}
		marker := trim[0]
		if marker != '+' && marker != '-' && marker != '*' {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(trim[1:]), name) {
			lines[i] = "+" + strings.TrimSpace(trim[1:])
			found = true
		}
	}
	if !found {
		lines = append([]string{"+" + name}, lines...)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, eol)), 0o644)
}

// enablePlugins re-checks the given plugins in plugins.txt, keeping their
// position when the line survived the removal, and appends missing ones.
// loadorder.txt gets the names too, since an enabled plugin absent from it is not
// loaded by MO2.
func enablePlugins(m *Mo2Instance, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	want := map[string]string{}
	for _, n := range names {
		want[strings.ToLower(n)] = n
	}
	var enabled []string

	if m.PluginsTxt != "" {
		raw, err := os.ReadFile(m.PluginsTxt)
		if err != nil {
			return nil, fmt.Errorf("read plugins.txt: %w", err)
		}
		text := string(raw)
		eol := "\n"
		if strings.Contains(text, "\r\n") {
			eol = "\r\n"
		}
		lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
		for i, line := range lines {
			trim := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
			body := strings.TrimSpace(strings.TrimPrefix(trim, "*"))
			key := strings.ToLower(body)
			if _, ok := want[key]; ok {
				lines[i] = "*" + body
				enabled = append(enabled, body)
				delete(want, key)
			}
		}
		for _, name := range sortedValues(want) {
			lines = append(lines, "*"+name)
			enabled = append(enabled, name)
		}
		if err := os.WriteFile(m.PluginsTxt, []byte(strings.Join(lines, eol)), 0o644); err != nil {
			return nil, fmt.Errorf("write plugins.txt: %w", err)
		}
	}

	if m.LoadOrderTxt != "" && pathExists(m.LoadOrderTxt) {
		raw, err := os.ReadFile(m.LoadOrderTxt)
		if err == nil {
			text := string(raw)
			eol := "\n"
			if strings.Contains(text, "\r\n") {
				eol = "\r\n"
			}
			lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
			present := map[string]bool{}
			for _, line := range lines {
				present[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "\ufeff")))] = true
			}
			added := false
			for _, name := range enabled {
				if !present[strings.ToLower(name)] {
					lines = append(lines, name)
					added = true
				}
			}
			if added {
				_ = os.WriteFile(m.LoadOrderTxt, []byte(strings.Join(lines, eol)), 0o644)
			}
		}
	}
	return enabled, nil
}
