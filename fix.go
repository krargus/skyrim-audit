package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// FixResult describes what DisablePlugins changed.
type FixResult struct {
	Disabled []string `json:"disabled"`
	Skipped  []string `json:"skipped,omitempty"`
	Backup   string   `json:"backup,omitempty"`
}

// DisablePlugins unchecks plugins in plugins.txt. The names stay in the file and
// only the leading asterisk goes, which is exactly what MO2 writes for an
// unchecked plugin, so re-enabling one later restores its position. A backup is
// kept the first time the file is changed. Names not present as enabled entries
// are reported as skipped and never added.
func DisablePlugins(m *Mo2Instance, names []string) (*FixResult, error) {
	if m.PluginsTxt == "" {
		return nil, fmt.Errorf("no plugins.txt for this profile")
	}
	want := map[string]string{}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			want[strings.ToLower(n)] = n
		}
	}
	if len(want) == 0 {
		return nil, fmt.Errorf("no plugins to disable")
	}

	raw, err := os.ReadFile(m.PluginsTxt)
	if err != nil {
		return nil, fmt.Errorf("read plugins.txt: %w", err)
	}
	text := string(raw)
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}

	var out, disabled []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trim := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		body := strings.TrimSpace(strings.TrimPrefix(trim, "*"))
		key := strings.ToLower(body)
		if _, ok := want[key]; ok && strings.HasPrefix(trim, "*") {
			out = append(out, body)
			disabled = append(disabled, body)
			delete(want, key)
			continue
		}
		out = append(out, line)
	}
	if len(disabled) == 0 {
		return &FixResult{Skipped: sortedValues(want)}, nil
	}

	backup, err := backupFile(m.PluginsTxt)
	if err != nil {
		return nil, fmt.Errorf("backup plugins.txt: %w", err)
	}
	if err := os.WriteFile(m.PluginsTxt, []byte(strings.Join(out, eol)), 0o644); err != nil {
		return nil, fmt.Errorf("write plugins.txt: %w", err)
	}
	return &FixResult{Disabled: disabled, Skipped: sortedValues(want), Backup: backup}, nil
}

func sortedValues(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
