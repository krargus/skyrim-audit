package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixRequest(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8721/api/fix", strings.NewReader(body))
	r.Host = "127.0.0.1:8721"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// Naming a plugin whose master only needs enabling must be refused: the request
// gets a 400 and plugins.txt is untouched.
func TestHandleFixRefusesRepairablePlugin(t *testing.T) {
	f := newFixture(t)
	f.dataPlugin("Skyrim.esm", buildPlugin(flagMaster, 1.7, nil, 2))
	f.modPlugin("Base", "Base.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modPlugin("M", "Needy.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm", "Base.esp"}, 3))
	f.modPlugin("M", "Fine.esp", buildPlugin(0, 1.7, []string{"Skyrim.esm"}, 3))
	f.modlist("-Base", "+M")
	f.plugins("*Skyrim.esm", "*Needy.esp", "*Fine.esp")

	inst, err := OpenInstance(f.dir, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	before := readText(t, inst.PluginsTxt)
	s := NewServer(inst, Options{SaveDirs: []string{t.TempDir()}}, "skyrimspecialedition", false)

	w := fixRequest(t, s, `{"plugins":["Needy.esp"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %q; want 400", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "master is enabled") {
		t.Errorf("refusal should explain the real fix: %q", w.Body.String())
	}
	if got := readText(t, inst.PluginsTxt); got != before {
		t.Errorf("plugins.txt changed: %q", got)
	}

	// A healthy plugin is refused for the same reason: it is not broken.
	w = fixRequest(t, s, `{"plugins":["Fine.esp"]}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d for a healthy plugin; want 400", w.Code)
	}
}

// A plugin whose master is installed nowhere can be unchecked, and only that one.
func TestHandleFixDisablesUnrepairablePlugin(t *testing.T) {
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
	s := NewServer(inst, Options{SaveDirs: []string{t.TempDir()}}, "skyrimspecialedition", false)

	w := fixRequest(t, s, `{"plugins":["Needy.esp","Fine.esp"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q; want 200", w.Code, w.Body.String())
	}
	var out FixResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (%q)", err, w.Body.String())
	}
	if len(out.Disabled) != 1 || out.Disabled[0] != "Needy.esp" {
		t.Errorf("disabled = %v, want only [Needy.esp]", out.Disabled)
	}
	text := readText(t, inst.PluginsTxt)
	if !strings.Contains(text, "\nNeedy.esp") || strings.Contains(text, "*Needy.esp") {
		t.Errorf("Needy.esp should be unchecked: %q", text)
	}
	if !strings.Contains(text, "*Fine.esp") {
		t.Errorf("Fine.esp must stay enabled even though the request named it: %q", text)
	}
}
