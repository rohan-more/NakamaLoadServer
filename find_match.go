package main

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)

type findMatchResponse struct {
	MatchID string `json:"match_id"`
	Created bool   `json:"created"`
}

func rpcFindMatch(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	if _, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string); !ok {
		return "", errNoUserIdFound
	}

	query := "+label.open:1 +label.mode:shooter"
	minSize := 0
	maxSize := maxPlayers - 1

	matches, err := nk.MatchList(ctx, 10, true, "", &minSize, &maxSize, query)
	if err != nil {
		logger.Error("MatchList error: %v", err)
		return "", errInternalError
	}

	resp := findMatchResponse{}

	if len(matches) > 0 {
		resp.MatchID = matches[0].MatchId
		resp.Created = false
	} else {
		matchID, err := nk.MatchCreate(ctx, "shooter", map[string]interface{}{})
		if err != nil {
			logger.Error("MatchCreate error: %v", err)
			return "", errInternalError
		}
		resp.MatchID = matchID
		resp.Created = true
	}

	out, err := json.Marshal(resp)
	if err != nil {
		logger.Error("Marshal error: %v", err)
		return "", errMarshal
	}

	logger.Info("find_match -> %v (created: %v)", resp.MatchID, resp.Created)
	return string(out), nil
}