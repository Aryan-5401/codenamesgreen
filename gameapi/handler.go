package gameapi

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"codenamesgreen/dictionary-master"
)

// Handler implements the codenames green server handler.
func Handler(wordLists map[string][]string) http.Handler {
	h := &handler{
		mux:       http.NewServeMux(),
		wordLists: wordLists,
		rand:      rand.New(rand.NewSource(time.Now().UnixNano())),
		games:     make(map[string]*Game),
	}

	// Build a list of all words. The combined list
	// of words is our default word list for new games,
	// and the set of words we draw from for game IDs.
	m := map[string]bool{}
	for _, list := range wordLists {
		for _, w := range list {
			if !m[w] {
				h.allWords = append(h.allWords, w)
				m[w] = true
			}
		}
	}
	sort.Strings(h.allWords)

	h.mux.HandleFunc("/index", h.handleIndex)
	h.mux.HandleFunc("/new-game", h.handleNewGame)
	h.mux.HandleFunc("/guess", h.handleGuess)
	h.mux.HandleFunc("/end-turn", h.handleEndTurn)
	h.mux.HandleFunc("/chat", h.handleChat)
	h.mux.HandleFunc("/events", h.handleEvents)
	h.mux.HandleFunc("/ping", h.handlePing)
	h.mux.HandleFunc("/stats", h.handleStats)

	// Periodically remove games that are old and inactive.
	go func() {
		for now := range time.Tick(10 * time.Minute) {
			h.mu.Lock()
			for id, g := range h.games {
				remaining := g.pruneOldPlayers(now)
				if remaining > 0 {
					continue // at least one player is still in the game
				}
				if g.CreatedAt.Add(24 * time.Hour).After(time.Now()) {
					continue // hasn't been 24 hours since the game started
				}
				delete(h.games, id)
			}
			h.mu.Unlock()
		}
	}()

	return h
}

type handler struct {
	mux       *http.ServeMux
	wordLists map[string][]string
	allWords  []string
	rand      *rand.Rand

	mu    sync.Mutex
	games map[string]*Game
}

func (h *handler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// Allow all cross-origin requests.
	header := rw.Header()
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("Access-Control-Allow-Methods", "*")
	header.Set("Access-Control-Allow-Headers", "Content-Type")
	header.Set("Access-Control-Max-Age", "1728000") // 20 days

	if req.Method == "OPTIONS" {
		rw.WriteHeader(http.StatusOK)
		return
	}
	h.mux.ServeHTTP(rw, req)
}

// POST /index
func (h *handler) handleIndex(rw http.ResponseWriter, req *http.Request) {
	// Autogenerate a game ID from the set of words that we know about, skipping
	// any that already have games in-memory.
	id := ""
	h.mu.Lock()
	for {
		w1 := strings.ToLower(h.allWords[h.rand.Int63n(int64(len(h.allWords)))])
		w2 := strings.ToLower(h.allWords[h.rand.Int63n(int64(len(h.allWords)))])
		id := fmt.Sprintf("%s-%s", w1, w2)
		if _, ok := h.games[id]; !ok {
			break
		}
	}
	h.mu.Unlock()

	writeJSON(rw, struct {
		AutogeneratedID string `json:"autogenerated_id"`
	}{id})
}

// POST /new-game
func (h *handler) handleNewGame(rw http.ResponseWriter, req *http.Request) {
	var body struct {
		GameID   string   `json:"game_id"`
		Words    []string `json:"words,omitempty"`
		PrevSeed *Seed    `json:"prev_seed,omitempty"` // a string because of js number precision
	}
	err := json.NewDecoder(req.Body).Decode(&body)
	if err != nil || body.GameID == "" {
		writeError(rw, "malformed_body", "Unable to parse request body.", 400)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// If the game already exists, make sure that the request includes
	// the existing game's seed so a delayed request doesn't reset an
	// existing game.
	oldGame, ok := h.games[body.GameID]
	if ok {
		oldGame.mu.Lock()
		defer oldGame.mu.Unlock()
	}
	if ok && (body.PrevSeed == nil || *body.PrevSeed != oldGame.Seed) {
		writeJSON(rw, oldGame)
		return
	}

	words := body.Words
	if len(words) == 0 {
		words = h.allWords
	}
	if len(words) < len(colorDistribution) {
		writeError(rw, "too_few_words",
			fmt.Sprintf("A word list must have at least %d words.", len(colorDistribution)), 400)
		return
	}

	game := ReconstructGame(NewState(h.rand.Int63(), words))
	if oldGame != nil {
		// Carry over the players but without teams in case
		// they want to switch them up.
		for id, p := range oldGame.players {
			game.players[id] = Player{LastSeen: p.LastSeen}
		}

		// Wake up any clients waiting on this game.
		oldGame.notifyAll()
	}

	g := &game
	g.CreatedAt = time.Now()
	h.games[body.GameID] = g
	writeJSON(rw, g)
}

// POST /guess
func (h *handler) handleGuess(rw http.ResponseWriter, req *http.Request) {
	var body struct {
		GameID   string `json:"game_id"`
		Seed     Seed   `json:"seed"`
		PlayerID string `json:"player_id"`
		Name     string `json:"name"`
		Team     int    `json:"team"`
		Index    int    `json:"index"`
	}

	err := json.NewDecoder(req.Body).Decode(&body)
	if err != nil || body.GameID == "" || body.Team == 0 || body.PlayerID == "" {
		writeError(rw, "malformed_body", "Unable to parse request body.", 400)
		return
	}

	h.mu.Lock()
	g, ok := h.games[body.GameID]
	h.mu.Unlock()
	if !ok {
		writeError(rw, "not_found", "Game not found", 404)
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if body.Seed != g.Seed {
		writeError(rw, "bad_seed", "Request intended for a different game seed.", 400)
		return
	}

	g.markSeen(body.PlayerID, body.Name, body.Team, time.Now())
	g.guess(body.PlayerID, body.Name, body.Team, body.Index, time.Now())
	writeJSON(rw, map[string]string{"status": "ok"})
}

// POST /end-turn
func (h *handler) handleEndTurn(rw http.ResponseWriter, req *http.Request) {
	var body struct {
		GameID   string `json:"game_id"`
		Seed     Seed   `json:"seed"`
		PlayerID string `json:"player_id"`
		Name     string `json:"name"`
		Team     int    `json:"team"`
	}

	err := json.NewDecoder(req.Body).Decode(&body)
	if err != nil || body.GameID == "" || body.Team == 0 || body.PlayerID == "" {
		writeError(rw, "malformed_body", "Unable to parse request body.", 400)
		return
	}

	h.mu.Lock()
	g, ok := h.games[body.GameID]
	h.mu.Unlock()
	if !ok {
		writeError(rw, "not_found", "Game not found", 404)
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if body.Seed != g.Seed {
		writeError(rw, "bad_seed", "Request intended for a different game seed.", 400)
		return
	}

	g.markSeen(body.PlayerID, body.Name, body.Team, time.Now())
	g.addEvent(Event{
		Type:     "end_turn",
		Team:     body.Team,
		PlayerID: body.PlayerID,
		Name:     body.Name,
	})
	writeJSON(rw, map[string]string{"status": "ok"})
}

// POST /chat
func (h *handler) handleChat(rw http.ResponseWriter, req *http.Request) {
	var body struct {
		GameID   string `json:"game_id"`
		Seed     Seed   `json:"seed"`
		PlayerID string `json:"player_id"`
		Name     string `json:"name"`
		Team     int    `json:"team"`
		Message  string `json:"message"`
	}

	err := json.NewDecoder(req.Body).Decode(&body)
	if err != nil || body.GameID == "" || body.Team == 0 || body.PlayerID == "" || body.Message == "" {
		writeError(rw, "malformed_body", "Unable to parse request body.", 400)
		return
	}

	h.mu.Lock()
	g, ok := h.games[body.GameID]
	h.mu.Unlock()
	if !ok {
		writeError(rw, "not_found", "Game not found", 404)
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if body.Seed != g.Seed {
		writeError(rw, "bad_seed", "Request intended for a different game seed.", 400)
		return
	}

	g.markSeen(body.PlayerID, body.Name, body.Team, time.Now())
	g.addEvent(Event{
		Type:     "chat",
		Team:     body.Team,
		PlayerID: body.PlayerID,
		Name:     body.Name,
		Message:  body.Message,
	})
	writeJSON(rw, map[string]string{"status": "ok"})
}

// POST /events
func (h *handler) handleEvents(rw http.ResponseWriter, req *http.Request) {
	var body struct {
		GameID    string `json:"game_id"`
		Seed      Seed   `json:"seed"`
		PlayerID  string `json:"player_id"`
		Name      string `json:"name"`
		Team      int    `json:"team"`
		LastEvent int    `json:"last_event"`
	}

	err := json.NewDecoder(req.Body).Decode(&body)
	if err != nil || body.GameID == "" || body.PlayerID == "" {
		writeError(rw, "malformed_body", "Unable to parse request body.", 400)
		return
	}

	h.mu.Lock()
	g, ok := h.games[body.GameID]
	h.mu.Unlock()
	if !ok {
		writeError(rw, "not_found", "Game not found", 404)
		return
	}

	g.mu.Lock()
	seed := g.Seed
	if body.Seed != seed {
		evts, _ := g.eventsSince(body.LastEvent)
		g.mu.Unlock()
		writeJSON(rw, GameUpdate{Seed: seed, Events: evts})
		return
	}
	g.markSeen(body.PlayerID, body.Name, body.Team, time.Now())

	evts, ch := g.eventsSince(body.LastEvent)

	// Release the mutex.
	// We reacquire it when we reretrieve the game.
	g.mu.Unlock()

	if len(evts) > 0 {
		writeJSON(rw, GameUpdate{Seed: seed, Events: evts})
		return
	}

	// Wait until a new event becomes available, the client
	// gives up, or we time out.
	select {
	case <-ch:
		// re-retrieve the game in case it was replaced
		// while we were waiting for events.
		h.mu.Lock()
		g, ok := h.games[body.GameID]
		h.mu.Unlock()
		if !ok {
			writeError(rw, "not_found", "Game not found", 404)
			return
		}
		g.mu.Lock()
		evts, _ = g.eventsSince(body.LastEvent)
		seed = g.Seed
		g.mu.Unlock()

	case <-req.Context().Done():
	case <-time.After(25 * time.Second):
	}
	writeJSON(rw, GameUpdate{Seed: seed, Events: evts})
}

// POST /ping
// This endpoint is a convenient way to record updates to player config
// without waiting for the long-polling loop to make a new request.
// It only calls `markSeen` with the provided player information
// and has no other effects.
func (h *handler) handlePing(rw http.ResponseWriter, req *http.Request) {
	var body struct {
		GameID   string `json:"game_id"`
		Seed     Seed   `json:"seed"`
		PlayerID string `json:"player_id"`
		Name     string `json:"name"`
		Team     int    `json:"team"`
	}

	err := json.NewDecoder(req.Body).Decode(&body)
	if err != nil || body.GameID == "" || body.PlayerID == "" {
		writeError(rw, "malformed_body", "Unable to parse request body.", 400)
		return
	}

	h.mu.Lock()
	g, ok := h.games[body.GameID]
	h.mu.Unlock()
	if !ok {
		writeError(rw, "not_found", "Game not found", 404)
		return
	}
	if body.Seed != g.Seed {
		writeError(rw, "bad_seed", "Request intended for a different game seed.", 400)
		return
	}

	g.mu.Lock()
	g.markSeen(body.PlayerID, body.Name, body.Team, time.Now())
	g.mu.Unlock()
	writeJSON(rw, map[string]string{"status": "ok"})
}

type GameUpdate struct {
	Seed   Seed    `json:"seed"`
	Events []Event `json:"events"`
}

func (h *handler) handleStats(rw http.ResponseWriter, req *http.Request) {
	var players, games int
	h.mu.Lock()
	for _, g := range h.games {
		g.mu.Lock()
		players += len(g.players)
		if len(g.players) > 0 {
			games++
		}
		g.mu.Unlock()
	}
	h.mu.Unlock()

	writeJSON(rw, struct {
		ActiveGames   int `json:"active_games"`
		ActivePlayers int `json:"active_players"`
	}{ActiveGames: games, ActivePlayers: players})
}

func writeError(rw http.ResponseWriter, code, message string, statusCode int) {
	rw.WriteHeader(statusCode)
	writeJSON(rw, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}

func writeJSON(rw http.ResponseWriter, resp interface{}) {
	j, err := json.Marshal(resp)
	if err != nil {
		http.Error(rw, "unable to marshal response: "+err.Error(), 500)
		return
	}

	rw.Header().Set("Content-Type", "application/json")
	rw.Write(j)
}

func DefaultWordlists() (map[string][]string, error) {
	matches, err := filepath.Glob("wordlists/*txt")
	if err != nil {
		return nil, err
	}

	lists := map[string][]string{}
	for _, m := range matches {
		base := filepath.Base(m)
		name := strings.TrimSuffix(base, filepath.Ext(base))

		d, err := dictionary.Load(m)
		if err != nil {
			return nil, err
		}
		words := d.Words()
		sort.Strings(words)
		lists[name] = words
	}
	return lists, nil
}
