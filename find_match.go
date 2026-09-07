package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

	"github.com/heroiclabs/nakama-common/runtime"
)

type findMatchResponse struct {
	MatchID string `json:"match_id"`
	Created bool   `json:"created"`
}

// Nakama's match listing index lags behind MatchCreate, so a match created a
// moment ago is not yet visible to MatchList. Without this, callers arriving
// together each see an empty list and each create their own match instead of
// pairing up. The mutex serialises the find-or-create decision and lastCreated
// hands the freshly made match to the next caller directly.
var (
	matchmakingMu sync.Mutex
	lastCreated   string
)

func rpcFindMatch(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	if _, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string); !ok {
		return "", errNoUserIdFound
	}

	matchmakingMu.Lock()
	defer matchmakingMu.Unlock()

	resp := findMatchResponse{}

	switch matchID, err := findJoinable(ctx, logger, nk); {
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

	out, err := json.Marshal(resp)
	if err != nil {
		logger.Error("Marshal error: %v", err)
		return "", errMarshal
	}

	logger.Info("find_match -> %v (created: %v)", resp.MatchID, resp.Created)
	return string(out), nil
}

// findJoinable returns a match with room for another player, or "" if there
// isn't one. Callers must hold matchmakingMu.
func findJoinable(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) (string, error) {
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
		case match.Size < maxPlayers && labelIsOpen(match.GetLabel().GetValue()):
			return lastCreated, nil
		default:
			// Full, or finished and serving out its grace period. Either way
			// it is no longer joinable, so fall through to the listing.
			lastCreated = ""
		}
	}

	minSize, maxSize := 0, maxPlayers-1
	matches, err := nk.MatchList(ctx, 10, true, "", &minSize, &maxSize, "+label.open:1 +label.mode:shooter")
	if err != nil {
		logger.Error("MatchList error: %v", err)
		return "", errInternalError
	}
	if len(matches) > 0 {
		return matches[0].MatchId, nil
	}

	return "", nil
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
