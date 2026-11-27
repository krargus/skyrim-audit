package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// quarantineDirName is where removed mods go. Deleting a mod outright is also
// supported, but moving it aside is the default because a load order is easy to
// break and hard to rebuild.
const quarantineDirName = "_skyrim-audit_quarantine"

// Removal describes what RemoveMod changed.
type Removal struct {
	Mod             string   `json:"mod"`
	Mode            string   `json:"mode"`
	Path            string   `json:"path"`
	NewPath         string   `json:"new_path,omitempty"`
	Files           int      `json:"files,omitempty"`
	UncheckedPlugin []string `json:"unchecked_plugins,omitempty"`
	Backups         []string `json:"backups,omitempty"`
}

// RemoveMod takes a mod out of the profile: its plugins are unchecked, its
// plugins.txt, loadorder.txt and modlist.txt entries are cleaned up, and the
// folder is either moved into a quarantine folder or deleted.
func RemoveMod(m *Mo2Instance, name, mode string) (*Removal, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("no mod name given")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\:`) {
		return nil, fmt.Errorf("%q is not a mod folder name", name)
	}
	if mode != "delete" {
		mode = "quarantine"
	}

	modPath := filepath.Join(m.ModsDir, name)
	if rel, err := filepath.Rel(m.ModsDir, modPath); err != nil || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("%q is outside the mods folder", name)
	}
	st, err := os.Stat(modPath)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("mod folder not found: %s", modPath)
	}

	// Collect the plugins this mod ships before it moves, so plugins.txt can be
	// cleaned without rescanning afterwards.
	shipped := map[string]bool{}
	budget := 20000
	collectPluginNames(modPath, shipped, &budget)

	out := &Removal{Mod: name, Mode: mode, Path: modPath}

	for _, path := range []string{m.ModListTxt, m.PluginsTxt, m.LoadOrderTxt} {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if bak, err := backupFile(path); err == nil && bak != "" {
			out.Backups = append(out.Backups, bak)
		}
	}

	if err := disableModInModList(m.ModListTxt, name); err != nil {
		return nil, err
	}
	if unchecked, err := stripPlugins(m.PluginsTxt, shipped); err != nil {
		return nil, err
	} else {
		out.UncheckedPlugin = unchecked
	}
	if _, err := stripPlugins(m.LoadOrderTxt, shipped); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	out.Files = countFiles(modPath)

	if mode == "delete" {
		if err := os.RemoveAll(modPath); err != nil {
			return nil, fmt.Errorf("delete %s: %w", modPath, err)
		}
		return out, nil
	}

	dest := filepath.Join(m.ModsDir, quarantineDirName)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, fmt.Errorf("create quarantine folder: %w", err)
	}
	target := filepath.Join(dest, name)
	for i := 2; pathExists(target); i++ {
		target = filepath.Join(dest, fmt.Sprintf("%s (%d)", name, i))
	}
	if err := os.Rename(modPath, target); err != nil {
		return nil, fmt.Errorf("move %s to quarantine: %w", modPath, err)
	}
	out.NewPath = target
	// Remember where it went, so the UI can offer to put it back.
	recordRemoval(m, out)
	return out, nil
}

func collectPluginNames(dir string, set map[string]bool, budget *int) {
	if *budget <= 0 {
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
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if e.Name() == ".git" {
				continue
			}
			collectPluginNames(full, set, budget)
			continue
		}
		if isPluginFile(e.Name()) {
			set[strings.ToLower(e.Name())] = true
			*budget--
		}
	}
}

// disableModInModList turns "+Mod" into "-Mod" (keeping its position) so MO2
// shows it unchecked instead of leaving an unmanaged folder behind.
func disableModInModList(path, name string) error {
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
	eol := "\n"
	if strings.Contains(string(raw), "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	found := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if len(trim) < 2 {
			continue
		}
		marker := trim[0]
		if marker != '+' && marker != '-' && marker != '*' {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(trim[1:]), name) {
			lines[i] = "-" + strings.TrimSpace(trim[1:])
			found = true
		}
	}
	if !found {
		lines = append([]string{"-" + name}, lines...)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, eol)), 0o644)
}

// stripPlugins removes the given plugins from plugins.txt or loadorder.txt and
// reports which enabled plugins it unchecked.
func stripPlugins(path string, shipped map[string]bool) ([]string, error) {
	if path == "" || len(shipped) == 0 {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	eol := "\n"
	if strings.Contains(string(raw), "\r\n") {
		eol = "\r\n"
	}
	var out []string
	var removed []string
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		trim := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		body := strings.TrimSpace(strings.TrimPrefix(trim, "*"))
		if body == "" || strings.HasPrefix(body, "#") {
			out = append(out, line)
			continue
		}
		if shipped[strings.ToLower(body)] {
			removed = append(removed, body)
			continue
		}
		out = append(out, line)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if err := os.WriteFile(path, []byte(strings.Join(out, eol)), 0o644); err != nil {
		return nil, err
	}
	return removed, nil
}

// backupFile keeps the first pre-change copy of a profile file so the user can
// undo a removal by hand.
func backupFile(path string) (string, error) {
	bak := path + ".skyrim-audit.bak"
	if pathExists(bak) {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(bak, data, 0o644); err != nil {
		return "", err
	}
	return bak, nil
}

func countFiles(dir string) int {
	n := 0
	budget := 200000
	var walk func(string)
	walk = func(d string) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			if budget <= 0 {
				return
			}
			if e.IsDir() {
				walk(filepath.Join(d, e.Name()))
				continue
			}
			n++
			budget--
		}
	}
	walk(dir)
	return n
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
