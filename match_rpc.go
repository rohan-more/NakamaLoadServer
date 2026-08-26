package main

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)

type createMatchResponse struct {
	MatchID string `json:"match_id"`
}

// Clients can't create authoritative matches directly: socket.CreateMatchAsync()
// makes a relayed match with no server handler. Creating one has to go through
// nk.MatchCreate here, and the client then joins the returned id over the socket.
func rpcCreateMatch(ctx context.Context, logger runtime.Logger, db *sql.DB,
	nk runtime.NakamaModule, payload string) (string, error) {
	if _, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string); !ok {
		return "", errNoUserIdFound
	}

	matchID, err := nk.MatchCreate(ctx, moduleName, nil)
	if err != nil {
		logger.Error("MatchCreate error: %v", err)
		return "", errInternalError
	}

	out, err := json.Marshal(createMatchResponse{MatchID: matchID})
	if err != nil {
		logger.Error("Marshal error: %v", err)
		return "", errMarshal
	}

	logger.Info("Created match %v", matchID)
	return string(out), nil
}
