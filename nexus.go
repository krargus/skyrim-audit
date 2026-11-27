package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// nexusGraphQL is the public Nexus Mods v2 endpoint. Mod search there answers
// without an API key, which keeps this tool usable out of the box.
const nexusGraphQL = "https://api.nexusmods.com/v2/graphql"

const nexusSearchQuery = `query Search($filter: ModsFilter, $sort: [ModsSort!], $count: Int) {
  mods(filter: $filter, sort: $sort, count: $count) {
    totalCount
    nodes { modId name summary version author category updatedAt downloads endorsements status thumbnailUrl }
  }
}`

// NexusCard is the artwork a mod list shows next to an installed mod.
type NexusCard struct {
	ID        int    `json:"id"`
	Thumbnail string `json:"thumbnail,omitempty"`
}

const nexusRequirementQuery = `query Requirements($m: ID!, $g: ID!) {
  mod(modId: $m, gameId: $g) {
    modRequirements {
      nexusRequirements { nodes { modId modName } }
    }
  }
}`

const nexusGameQuery = `query Game($d: String!) { game(domainName: $d) { id name } }`

// NexusRequirement is one mod another mod declares it needs.
type NexusRequirement struct {
	ModID int    `json:"id"`
	Name  string `json:"name"`
}

// NexusMod is one Nexus Mods search hit, reduced to what a load-order problem
// needs: what the mod is, how popular it is, and where its page lives.
type NexusMod struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Summary      string `json:"summary"`
	Version      string `json:"version"`
	Author       string `json:"author"`
	Category     string `json:"category"`
	Updated      string `json:"updated"`
	Downloads    int    `json:"downloads"`
	Endorsements int    `json:"endorsements"`
	URL          string `json:"url"`
	Thumbnail    string `json:"thumbnail,omitempty"`
	Score        int    `json:"score"`
	Why          string `json:"why,omitempty"`
}

type nexusCacheEntry struct {
	at   time.Time
	mods []NexusMod
}

// NexusClient searches Nexus Mods and remembers answers for a few minutes, so
// opening several findings that share a mod does not repeat the request.
type NexusClient struct {
	endpoint string
	game     string
	http     *http.Client
	disabled bool

	mu      sync.Mutex
	cache   map[string]nexusCacheEntry
	reqs    map[int]nexusReqEntry
	cards   map[int]nexusCardEntry
	gameIDv int
	last    time.Time
}

type nexusReqEntry struct {
	at    time.Time
	items []NexusRequirement
}

type nexusCardEntry struct {
	at   time.Time
	card NexusCard
}

func NewNexusClient(game string, disabled bool) *NexusClient {
	if game == "" {
		game = "skyrimspecialedition"
	}
	c := &NexusClient{
		endpoint: nexusGraphQL,
		game:     game,
		disabled: disabled,
		cache:    map[string]nexusCacheEntry{},
		reqs:     map[int]nexusReqEntry{},
		cards:    map[int]nexusCardEntry{},
		http:     &http.Client{Timeout: 25 * time.Second},
	}
	c.loadDisk()
	return c
}

// nexusDiskCache carries requirement answers between runs. Without it every CLI
// audit repeats the same lookups, and an audit on a flaky connection cannot use
// what it already learned.
type nexusDiskCache struct {
	Game   string                        `json:"game"`
	GameID int                           `json:"game_id,omitempty"`
	Saved  time.Time                     `json:"saved"`
	Reqs   map[string][]NexusRequirement `json:"requirements,omitempty"`
}

// nexusDiskTTL is how long a saved answer stays usable. Requirement lists change
// rarely, and a stale one can only keep an expected override out of the report.
const nexusDiskTTL = 7 * 24 * time.Hour

func nexusCachePath() string {
	p := settingsPath()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "nexus-cache.json")
}

// loadDisk seeds this run from the cache. Anything unreadable, for another game
// domain, or older than the TTL is simply ignored.
func (c *NexusClient) loadDisk() {
	if c.disabled {
		return
	}
	p := nexusCachePath()
	if p == "" {
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var d nexusDiskCache
	if json.Unmarshal(raw, &d) != nil || d.Game != c.game || time.Since(d.Saved) > nexusDiskTTL {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gameIDv = d.GameID
	for key, items := range d.Reqs {
		id, err := strconv.Atoi(key)
		if err != nil || id <= 0 {
			continue
		}
		c.reqs[id] = nexusReqEntry{at: time.Now(), items: items}
	}
}

// Search looks mods up by name and ranks them for a load-order fix. needle is an
// optional plugin filename: a hit whose description mentions it is almost always
// the mod that ships that plugin, or a patch for it.
func (c *NexusClient) Search(query, needle string) ([]NexusMod, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty search")
	}
	if c.disabled {
		return nil, fmt.Errorf("Nexus search is disabled (-no-nexus)")
	}
	key := strings.ToLower(query + "\x00" + needle)
	if mods, ok := c.cached(key); ok {
		return mods, nil
	}

	mods, err := c.fetch(query, false)
	if err == nil && len(mods) == 0 {
		// Nothing matched by name; the description field is the wider net and is
		// what usually finds "patch for X" style mods.
		mods, err = c.fetch(query, true)
	}
	if err != nil {
		return nil, err
	}

	ranked := rankNexus(mods, query, needle)
	c.store(key, ranked)
	return ranked, nil
}

func (c *NexusClient) cached(key string) ([]NexusMod, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok || time.Since(e.at) > 10*time.Minute {
		return nil, false
	}
	return e.mods, true
}

func (c *NexusClient) store(key string, mods []NexusMod) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = nexusCacheEntry{at: time.Now(), mods: mods}
}

// throttle keeps unauthenticated, user-based rate limits happy.
func (c *NexusClient) throttle() {
	c.mu.Lock()
	wait := 400*time.Millisecond - time.Since(c.last)
	c.last = time.Now().Add(wait)
	c.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}

type gqlMod struct {
	ModID        int    `json:"modId"`
	Name         string `json:"name"`
	Summary      string `json:"summary"`
	Version      string `json:"version"`
	Author       string `json:"author"`
	Category     string `json:"category"`
	UpdatedAt    string `json:"updatedAt"`
	Downloads    int    `json:"downloads"`
	Endorsements int    `json:"endorsements"`
	Status       string `json:"status"`
	ThumbnailURL string `json:"thumbnailUrl"`
}

type gqlResponse struct {
	Data struct {
		Mods struct {
			TotalCount int      `json:"totalCount"`
			Nodes      []gqlMod `json:"nodes"`
		} `json:"mods"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *NexusClient) fetch(query string, inDescription bool) ([]NexusMod, error) {
	field := "name"
	op := "WILDCARD"
	if inDescription {
		field = "description"
		op = "MATCHES"
	}
	vars := map[string]any{
		"filter": map[string]any{
			field:            []map[string]string{{"value": query, "op": op}},
			"gameDomainName": []map[string]string{{"value": c.game, "op": "EQUALS"}},
		},
		"sort":  []map[string]any{{"endorsements": map[string]string{"direction": "DESC"}}},
		"count": 40,
	}

	var out gqlResponse
	if err := c.post(nexusSearchQuery, vars, &out); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("nexus search: %s", out.Errors[0].Message)
	}

	mods := make([]NexusMod, 0, len(out.Data.Mods.Nodes))
	for _, n := range out.Data.Mods.Nodes {
		if n.Status != "" && n.Status != "published" {
			continue
		}
		mods = append(mods, NexusMod{
			ID:           n.ModID,
			Name:         n.Name,
			Summary:      strings.TrimSpace(n.Summary),
			Version:      n.Version,
			Author:       n.Author,
			Category:     n.Category,
			Updated:      n.UpdatedAt,
			Downloads:    n.Downloads,
			Endorsements: n.Endorsements,
			URL:          nexusModURL(c.game, n.ModID),
			Thumbnail:    n.ThumbnailURL,
		})
	}
	return mods, nil
}

// post sends one GraphQL query, with the shared rate limit applied.
func (c *NexusClient) post(query string, vars map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}

	c.throttle()
	req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "skyrim-audit/1.0 (local load-order auditor)")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("nexus request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("nexus request returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// gameID resolves the numeric game id the mod query needs, once per run.
func (c *NexusClient) gameID() (int, error) {
	c.mu.Lock()
	cached := c.gameIDv
	c.mu.Unlock()
	if cached > 0 {
		return cached, nil
	}
	var out struct {
		Data struct {
			Game struct {
				ID int `json:"id"`
			} `json:"game"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.post(nexusGameQuery, map[string]any{"d": c.game}, &out); err != nil {
		return 0, err
	}
	if len(out.Errors) > 0 {
		return 0, fmt.Errorf("nexus game lookup: %s", out.Errors[0].Message)
	}
	if out.Data.Game.ID == 0 {
		return 0, fmt.Errorf("unknown Nexus game domain %q", c.game)
	}
	c.mu.Lock()
	c.gameIDv = out.Data.Game.ID
	c.mu.Unlock()
	c.saveDisk()
	return out.Data.Game.ID, nil
}

// saveDisk rewrites the cache from this run's answers. Best effort: a read-only
// config folder must not break an audit.
func (c *NexusClient) saveDisk() {
	if c.disabled {
		return
	}
	p := nexusCachePath()
	if p == "" {
		return
	}
	c.mu.Lock()
	d := nexusDiskCache{Game: c.game, GameID: c.gameIDv, Saved: time.Now(), Reqs: make(map[string][]NexusRequirement, len(c.reqs))}
	for id, e := range c.reqs {
		d.Reqs[strconv.Itoa(id)] = e.items
	}
	c.mu.Unlock()

	raw, err := json.Marshal(d)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// Cards returns the Nexus artwork and short metadata for installed mods, looked
// up in one request per hundred ids and cached for half an hour. Ids without a
// page simply come back missing, so the UI can fall back to initials.
func (c *NexusClient) Cards(ids []int) (map[int]NexusCard, error) {
	cards := map[int]NexusCard{}
	if c.disabled {
		return cards, nil
	}

	seen := map[int]bool{}
	var missing []int
	c.mu.Lock()
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		if e, ok := c.cards[id]; ok && time.Since(e.at) < 30*time.Minute {
			cards[id] = e.card
			continue
		}
		missing = append(missing, id)
	}
	c.mu.Unlock()
	if len(missing) == 0 {
		return cards, nil
	}

	gameID, err := c.gameID()
	if err != nil {
		return cards, err
	}

	var firstErr error
	for start := 0; start < len(missing); start += cardBatchSize {
		end := start + cardBatchSize
		if end > len(missing) {
			end = len(missing)
		}
		got, err := c.fetchCards(gameID, missing[start:end])
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		c.mu.Lock()
		for id, card := range got {
			c.cards[id] = nexusCardEntry{at: time.Now(), card: card}
			cards[id] = card
		}
		c.mu.Unlock()
	}
	return cards, firstErr
}

// cardBatchSize is how many aliased mod lookups go into one query. The API's
// modId filter ANDs its values instead of ORing them, so a single filtered query
// cannot ask for several mods at once.
const cardBatchSize = 10

// fetchCards asks for several mods in one request using aliased fields. Only the
// thumbnail is requested: a mod list needs the picture, not another copy of the
// name and summary the search already shows.
func (c *NexusClient) fetchCards(gameID int, ids []int) (map[int]NexusCard, error) {
	var query strings.Builder
	query.WriteString("query Batch {")
	for i, id := range ids {
		// ids come from meta.ini and are validated as positive integers.
		fmt.Fprintf(&query, " m%d: mod(modId: %d, gameId: %d) { modId thumbnailUrl }", i, id, gameID)
	}
	query.WriteString(" }")

	var out struct {
		Data map[string]struct {
			ModID        int    `json:"modId"`
			ThumbnailURL string `json:"thumbnailUrl"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.post(query.String(), nil, &out); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("nexus artwork: %s", out.Errors[0].Message)
	}

	cards := make(map[int]NexusCard, len(ids))
	for _, n := range out.Data {
		if n.ModID <= 0 {
			continue
		}
		cards[n.ModID] = NexusCard{ID: n.ModID, Thumbnail: n.ThumbnailURL}
	}
	return cards, nil
}

// Requirements lists the Nexus mods a mod declares it needs. A mod listed here
// is a dependency, so files it overrides are being overridden on purpose.
func (c *NexusClient) Requirements(modID int) ([]NexusRequirement, error) {
	if c.disabled {
		return nil, fmt.Errorf("Nexus lookups are disabled (-no-nexus)")
	}
	if modID <= 0 {
		return nil, fmt.Errorf("no Nexus mod id")
	}
	c.mu.Lock()
	if e, ok := c.reqs[modID]; ok && time.Since(e.at) < 10*time.Minute {
		items := e.items
		c.mu.Unlock()
		return items, nil
	}
	c.mu.Unlock()

	gid, err := c.gameID()
	if err != nil {
		return nil, err
	}

	var out struct {
		Data struct {
			Mod struct {
				ModRequirements struct {
					NexusRequirements struct {
						Nodes []struct {
							ModID string `json:"modId"`
							Name  string `json:"modName"`
						} `json:"nodes"`
					} `json:"nexusRequirements"`
				} `json:"modRequirements"`
			} `json:"mod"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.post(nexusRequirementQuery, map[string]any{"m": modID, "g": gid}, &out); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("nexus requirements: %s", out.Errors[0].Message)
	}

	var items []NexusRequirement
	for _, n := range out.Data.Mod.ModRequirements.NexusRequirements.Nodes {
		id, err := strconv.Atoi(n.ModID)
		if err != nil || id <= 0 {
			continue
		}
		items = append(items, NexusRequirement{ModID: id, Name: n.Name})
	}
	c.mu.Lock()
	c.reqs[modID] = nexusReqEntry{at: time.Now(), items: items}
	c.mu.Unlock()
	c.saveDisk()
	return items, nil
}

func nexusModURL(game string, id int) string {
	return fmt.Sprintf("https://www.nexusmods.com/%s/mods/%d", game, id)
}

// rankNexus puts the most plausible fix first: a page that names the plugin file
// beats one that merely matches the query words, and popularity breaks ties.
func rankNexus(mods []NexusMod, query, needle string) []NexusMod {
	q := strings.ToLower(strings.TrimSpace(query))
	words := strings.Fields(q)
	needle = strings.ToLower(strings.TrimSpace(needle))

	seen := map[int]bool{}
	out := mods[:0]
	for _, m := range mods {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true

		name := strings.ToLower(m.Name)
		summary := strings.ToLower(m.Summary)
		score := 0
		why := ""

		if needle != "" && strings.Contains(name, needle) {
			score += 100
			why = "name mentions " + needle
		}
		if needle != "" && strings.Contains(summary, needle) {
			score += 40
			if why == "" {
				why = "description mentions " + needle
			}
		}
		if name == q {
			score += 200
			why = "exact name match"
		}
		hits := 0
		for _, w := range words {
			if w != "" && strings.Contains(name, w) {
				hits++
			}
		}
		if len(words) > 0 && hits == len(words) {
			score += 150
			if why == "" {
				why = "name matches every search word"
			}
		} else {
			score += hits * 8
		}
		// A translation reuses the original's name, so it would otherwise crowd
		// out the mod the user actually needs. Assume an English game unless the
		// query itself asks for a language.
		if isLocalization(name) && !isLocalization(q) {
			score -= 120
			if why == "name matches every search word" {
				why = "translation of the name match"
			}
		}
		for _, w := range words {
			if w != "" && strings.Contains(summary, w) {
				score += 3
			}
		}
		// Popularity, capped so it only breaks ties between comparable matches.
		score += minInt(m.Endorsements/400, 25)
		if score > 0 && why == "" {
			why = "popular categorised match"
		}

		m.Score, m.Why = score, why
		out = append(out, m)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Endorsements > out[j].Endorsements
	})
	return out
}

// nexusLanguageWords are the markers a translation or localization page puts in
// its title.
var nexusLanguageWords = []string{
	"translation", "translate", "traduccion", "traducción", "übersetzung", "ubersetzung",
	"localization", "localisation", "french", "spanish", "german", "italian", "russian",
	"polish", "japanese", "chinese", "korean", "portuguese", "hungarian", "czech",
	"turkish", "ukrainian", "dutch", "brasileiro", "espanol", "español",
}

func isLocalization(s string) bool {
	for _, w := range nexusLanguageWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
