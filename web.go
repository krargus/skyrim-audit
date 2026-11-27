package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/index.html
var webIndex []byte

// Server is the local web UI. It re-audits on every request so edits made in MO2
// show up on refresh.
type Server struct {
	inst  *Mo2Instance
	opts  Options
	nexus *NexusClient
	mu    sync.Mutex
}

func NewServer(inst *Mo2Instance, opts Options, nexusGame string, nexusEnabled bool) *Server {
	return &Server{inst: inst, opts: opts, nexus: NewNexusClient(nexusGame, !nexusEnabled)}
}

type auditPayload struct {
	*Result
	Game      GameVersion `json:"game"`
	NexusGame string      `json:"nexus_game"`
	NexusOn   bool        `json:"nexus_enabled"`
	Generated string      `json:"generated"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(webIndex)
	})
	mux.HandleFunc("/api/audit", s.handleAudit)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/mod", s.handleMod)
	mux.HandleFunc("/api/fix", s.handleFix)
	mux.HandleFunc("/api/removals", s.handleRemovals)
	mux.HandleFunc("/api/restore", s.handleRestore)
	mux.HandleFunc("/api/report", s.handleReport)
	return mux
}

// ListenAndServe binds the UI (loopback by default) and optionally opens it.
func (s *Server) ListenAndServe(addr string, open bool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ln, open)
}

// Serve runs the UI on an already bound listener.
func (s *Server) Serve(ln net.Listener, open bool) error {
	url := "http://" + displayAddr(ln.Addr().String())
	fmt.Printf("skyrim-audit UI: %s\n", url)
	fmt.Println("press Ctrl+C (or close this window) to stop")
	if open {
		openBrowser(url)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

// ServeOnFirstFreePort starts the UI on the first port in [port, port+9] that
// accepts a listener, so starting the program twice opens a second window beside
// the running copy instead of failing on a bind error. A free port is detected
// by binding, not by reading the error text, which Windows translates.
func (s *Server) ServeOnFirstFreePort(host string, port int, open bool) error {
	var lastErr error
	for p := port; p < port+10; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err != nil {
			lastErr = err
			continue
		}
		return s.Serve(ln, open)
	}
	return fmt.Errorf("ports %d-%d are all in use: %w", port, port+9, lastErr)
}

func displayAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		_ = exec.Command("open", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	res, err := Audit(s.inst, s.opts)
	s.mu.Unlock()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	game := GameVersion{EslLimit: res.EslLimit}
	if s.opts.Game != nil {
		game = *s.opts.Game
	}
	suppressRequiredOverrides(res, s.nexus)
	s.attachArtwork(res)
	writeJSON(w, auditPayload{
		Result:    res,
		Game:      game,
		NexusGame: s.nexus.game,
		NexusOn:   !s.nexus.disabled,
		Generated: time.Now().Format(time.RFC3339),
	})
}

// attachArtwork adds each installed mod's Nexus preview image, so the list shows
// what the mod looks like instead of only its folder name. A mod without a page
// stays without art and the UI draws initials instead.
func (s *Server) attachArtwork(res *Result) {
	if s.nexus.disabled || len(res.Mods) == 0 {
		return
	}
	ids := make([]int, 0, len(res.Mods))
	for _, m := range res.Mods {
		if m.NexusID > 0 {
			ids = append(ids, m.NexusID)
		}
	}
	if len(ids) == 0 {
		return
	}
	cards, err := s.nexus.Cards(ids)
	if len(cards) == 0 {
		if err != nil {
			log.Printf("nexus artwork: %v", err)
		}
		return
	}
	for i := range res.Mods {
		if card, ok := cards[res.Mods[i].NexusID]; ok {
			res.Mods[i].Thumbnail = card.Thumbnail
		}
	}
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "missing q", http.StatusBadRequest)
		return
	}
	mods, err := s.nexus.Search(q, strings.TrimSpace(r.URL.Query().Get("needle")))
	if err != nil {
		httpError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"query":   q,
		"game":    s.nexus.game,
		"results": mods,
	})
}

type modRequest struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	Typed  string `json:"typed"`
}

// guardDestructive rejects anything but a same-origin JSON request. Without it a
// page in another tab could post to this local server, and these requests move
// or delete folders.
func guardDestructive(r *http.Request) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return fmt.Errorf("expected application/json, got %q", ct)
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("unreadable Origin header")
	}
	if !strings.EqualFold(u.Host, r.Host) {
		return fmt.Errorf("cross-origin request refused")
	}
	return nil
}

func (s *Server) handleMod(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := guardDestructive(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var req modRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	// A permanent delete must be confirmed by typing the mod name; the UI asks
	// for it, and the check stays here so no request can skip the confirmation.
	if req.Action == "delete" && req.Typed != req.Name {
		http.Error(w, "to delete a mod permanently, repeat its exact folder name", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	out, err := RemoveMod(s.inst, req.Name, req.Action)
	s.mu.Unlock()
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

// handleFix unchecks plugins that cannot be repaired - a master they need is
// installed nowhere. Plugins that would load once their unchecked master is
// enabled are refused: disabling them would hide the real fix. The browser may
// only name plugins the audit itself listed, so a request can never disable
// something arbitrary, and only those already enabled in plugins.txt change.
func (s *Server) handleFix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := guardDestructive(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var req struct {
		Plugins []string `json:"plugins"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := Audit(s.inst, s.opts)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	allowed := map[string]bool{}
	for _, p := range res.DisableCandidates {
		allowed[strings.ToLower(p)] = true
	}
	names := req.Plugins
	if len(names) == 0 {
		names = res.DisableCandidates
	}
	var checked []string
	for _, p := range names {
		if allowed[strings.ToLower(p)] {
			checked = append(checked, p)
		}
	}
	if len(checked) == 0 {
		http.Error(w, "nothing to disable: these plugins are not broken, or they load once their master is enabled", http.StatusBadRequest)
		return
	}
	out, err := DisablePlugins(s.inst, checked)
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleRemovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	list := ListRemovals(s.inst)
	if list == nil {
		list = []RemovalRecord{}
	}
	writeJSON(w, map[string]any{"removals": list})
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := guardDestructive(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	out, err := RestoreMod(s.inst, req.Name)
	s.mu.Unlock()
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

// handleReport serves the same audit as a self-contained page to save or send.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	res, err := Audit(s.inst, s.opts)
	s.mu.Unlock()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	suppressRequiredOverrides(res, s.nexus)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="skyrim-audit-report.html"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(res.HTMLReport()))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func httpError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprint(w, err.Error())
}
