package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

type findMatchResponse struct {
	MatchID string `json:"match_id"`
	Created bool   `json:"created"`
}

// How long a handed out seat is held before it is assumed the client never
// joined. Long enough to cover the round trip and the socket join that follows
// it, short enough that an abandoned handout frees up quickly.
const seatTTL = 10 * time.Second

// Nakama's match listing index lags behind MatchCreate, so a match created a
// moment ago is not yet visible to MatchList. Without this, callers arriving
// together each see an empty list and each create their own match instead of
// pairing up. The mutex serialises the find-or-create decision and lastCreated
// hands the freshly made match to the next caller directly.
var (
	matchmakingMu sync.Mutex
	lastCreated   string
)

// seats covers the other half of the problem: find_match returns an id but the
// client joins over its socket afterwards, so a match still reads as empty
// while callers are on their way to it. Without holding a seat per handout,
// one free slot gets promised to every caller in a burst and all but one are
// rejected on arrival.
//
// Seats are keyed by user and released by MatchJoin when that user arrives.
// Holding them until expiry instead would count a player twice for the
// lifetime of the seat, once as a presence and once as a seat, and the match
// would read as full to everyone who came after them.
//
// seatsMu is separate from matchmakingMu so the match loop can release a seat
// without waiting on matchmaking. Lock order is matchmakingMu then seatsMu.
var (
	seatsMu sync.Mutex
	seats   = make(map[string]map[string]time.Time) // match id -> user id -> expiry
)

func rpcFindMatch(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errNoUserIdFound
	}

	matchmakingMu.Lock()
	defer matchmakingMu.Unlock()

	now := time.Now()
	sweepSeats(now)

	resp := findMatchResponse{}

	switch matchID, err := findJoinable(ctx, logger, nk, now); {
	case err != nil:
		return "", err
	case matchID != "":
		resp.MatchID, resp.Created = matchID, false
	default:
		matchID, err := nk.MatchCreate(ctx, moduleName, map[string]interface{}{})
		if err != nil {
			logger.Error("MatchCreate error: %v", err)
			return "", errInternalError
		}
		resp.MatchID, resp.Created = matchID, true
		lastCreated = matchID
	}

	holdSeat(resp.MatchID, userID, now.Add(seatTTL))

	out, err := json.Marshal(resp)
	if err != nil {
		logger.Error("Marshal error: %v", err)
		return "", errMarshal
	}

	logger.Info("find_match -> %v (created: %v)", resp.MatchID, resp.Created)
	return string(out), nil
}

// findJoinable returns a match with room for another player once seats already
// promised are counted, or "" if there isn't one. Callers must hold
// matchmakingMu.
func findJoinable(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, now time.Time) (string, error) {
	// The match we handed out last is the one least likely to be indexed yet,
	// so check it directly before falling back to the listing.
	if lastCreated != "" {
		match, err := nk.MatchGet(ctx, lastCreated)
		switch {
		case err != nil:
			logger.Warn("MatchGet %v error: %v", lastCreated, err)
			lastCreated = ""
		case match == nil:
			lastCreated = "" // gone, most likely reaped as idle
		case hasRoom(lastCreated, int(match.Size), now) && labelIsOpen(match.GetLabel().GetValue()):
			return lastCreated, nil
		default:
			// Full, spoken for, or finished and serving out its grace period.
			lastCreated = ""
		}
	}

	minSize, maxSize := 0, maxPlayers-1
	matches, err := nk.MatchList(ctx, 10, true, "", &minSize, &maxSize, "+label.open:1 +label.mode:shooter")
	if err != nil {
		logger.Error("MatchList error: %v", err)
		return "", errInternalError
	}
	for _, match := range matches {
		if hasRoom(match.MatchId, int(match.Size), now) {
			return match.MatchId, nil
		}
	}

	return "", nil
}

// hasRoom reports whether a match has a slot left once players still on their
// way are counted alongside the players who have already arrived.
func hasRoom(matchID string, size int, now time.Time) bool {
	return size+liveSeats(matchID, now) < maxPlayers
}

func holdSeat(matchID, userID string, expiry time.Time) {
	seatsMu.Lock()
	defer seatsMu.Unlock()
	held, ok := seats[matchID]
	if !ok {
		held = make(map[string]time.Time)
		seats[matchID] = held
	}
	held[userID] = expiry
}

// releaseSeat is called from MatchJoin once the user is present in the match,
// from which point they are counted by the match size instead.
func releaseSeat(matchID, userID string) {
	seatsMu.Lock()
	defer seatsMu.Unlock()
	held, ok := seats[matchID]
	if !ok {
		return
	}
	delete(held, userID)
	if len(held) == 0 {
		delete(seats, matchID)
	}
}

// liveSeats prunes expired seats for one match and returns what remains.
func liveSeats(matchID string, now time.Time) int {
	seatsMu.Lock()
	defer seatsMu.Unlock()
	return pruneLocked(matchID, now)
}

func pruneLocked(matchID string, now time.Time) int {
	held := seats[matchID]
	for userID, expiry := range held {
		if !expiry.After(now) {
			delete(held, userID)
		}
	}
	if len(held) == 0 {
		delete(seats, matchID)
		return 0
	}
	return len(held)
}

// sweepSeats keeps the map from growing without bound; matches that are never
// looked at again would otherwise hold their entry forever.
func sweepSeats(now time.Time) {
	seatsMu.Lock()
	defer seatsMu.Unlock()
	if len(seats) < 512 {
		return
	}
	for matchID := range seats {
		pruneLocked(matchID, now)
	}
}

// labelIsOpen reports whether a match label still advertises room. MatchList
// filters on this via the query, but the lastCreated fast path reads the
// label straight off the match and has to check it too.
func labelIsOpen(label string) bool {
	var parsed struct {
		Open int `json:"open"`
	}
	if err := json.Unmarshal([]byte(label), &parsed); err != nil {
		return false
	}
	return parsed.Open == 1
}
