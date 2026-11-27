package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// settings is the little state that lets a double-clicked exe find the same MO2
// instance it audited last time. It lives under the user's config folder rather
// than next to the exe, which may sit somewhere read-only.
type settings struct {
	Instance string `json:"instance"`
}

func settingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "skyrim-audit", "config.json")
}

func loadSettings() settings {
	var s settings
	p := settingsPath()
	if p == "" {
		return s
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(raw, &s)
	return s
}

// rememberInstance stores the instance folder so the next launch needs no flags.
// Failing to write it is not worth stopping an audit over.
func rememberInstance(dir string) {
	p := settingsPath()
	if p == "" || dir == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	raw, err := json.MarshalIndent(settings{Instance: dir}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p, append(raw, '\n'), 0o644)
}

// findInstance locates a Mod Organizer 2 instance without being told where it
// is: the remembered folder first, then the exe's own folder (a portable
// instance with the exe dropped into it), then the folders MO2 keeps its
// per-game instances in.
func findInstance() (string, error) {
	if s := loadSettings(); s.Instance != "" && hasIni(s.Instance) {
		return s.Instance, nil
	}

	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, dir, filepath.Dir(dir))
	}
	for _, root := range instanceRoots() {
		candidates = append(candidates, root)
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if e.IsDir() {
				candidates = append(candidates, filepath.Join(root, e.Name()))
			}
		}
	}
	for _, c := range candidates {
		if hasIni(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("no Mod Organizer 2 instance found - run " +
		"skyrim-audit.exe -instance \"C:\\path\\to\\ModOrganizer\" once and this program " +
		"will remember the folder")
}

// hasIni keeps the search cheap: OpenInstance, called right after, produces the
// precise error when the folder is an MO2 instance but cannot be opened.
func hasIni(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "ModOrganizer.ini"))
	return err == nil
}

func instanceRoots() []string {
	var roots []string
	for _, env := range []string{"LOCALAPPDATA", "APPDATA"} {
		if base := os.Getenv(env); base != "" {
			roots = append(roots, filepath.Join(base, "ModOrganizer"))
		}
	}
	return roots
}
