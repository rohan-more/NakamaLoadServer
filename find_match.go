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
//
// seats then covers the other half of the problem: find_match returns an id
// but the client joins over its socket afterwards, so a match still reads as
// empty while callers are on their way to it. Without holding a seat per
// handout, one free slot gets promised to every caller in a burst and all but
// one are rejected on arrival.
var (
	matchmakingMu sync.Mutex
	lastCreated   string
	seats         = make(map[string][]time.Time)
)

func rpcFindMatch(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	if _, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string); !ok {
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

	// Hold a seat for the caller until they actually join.
	seats[resp.MatchID] = append(seats[resp.MatchID], now.Add(seatTTL))

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

// hasRoom reports whether a match has a slot left once outstanding handouts
// are counted alongside the players who have already arrived.
func hasRoom(matchID string, size int, now time.Time) bool {
	return size+liveSeats(matchID, now) < maxPlayers
}

// liveSeats prunes expired handouts for one match and returns what remains.
func liveSeats(matchID string, now time.Time) int {
	held := seats[matchID]
	kept := held[:0]
	for _, expiry := range held {
		if expiry.After(now) {
			kept = append(kept, expiry)
		}
	}
	if len(kept) == 0 {
		delete(seats, matchID)
		return 0
	}
	seats[matchID] = kept
	return len(kept)
}

// sweepSeats keeps the map from growing without bound; matches that are never
// looked at again would otherwise hold their entry forever.
func sweepSeats(now time.Time) {
	if len(seats) < 512 {
		return
	}
	for matchID := range seats {
		liveSeats(matchID, now)
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
