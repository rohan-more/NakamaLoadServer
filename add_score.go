package main

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)


type playerScore struct {
	Score int64 `json:"score"`
}	

type addScoreRequest struct {
	Score int64 `json:"points"`
}
type addScoreResponse struct {
	NewScore int64 `json:"new_score"`
}

func rpcAddScore(ctx context.Context, logger runtime.Logger, db *sql.DB, 
	nk runtime.NakamaModule, payload string) (string, error) {		
		userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
		if !ok {
			return "", errNoUserIdFound
		}	
		var req addScoreRequest
		if err := json.Unmarshal([]byte(payload), &req); err != nil {
			logger.Error("Unmarshal error: %v", err)
			return "", errUnmarshal
		}	

		if req.Score <= 0 || req.Score > 100 {
			return "", runtime.NewError("Invalid score value. Must be between 1 and 100.", 3)
		}

		objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
			Collection: "score",
			Key:        "player_score",
			UserID:     userID,
	}})

	if err != nil {
		logger.Error("StorageRead error: %v", err)
		return "", errInternalError
	}		

	score := &playerScore{
		Score: 0,
	}		
	for _, object := range objects {
		switch object.GetKey() {
		case "player_score":
			if err := json.Unmarshal([]byte(object.GetValue()), score); err != nil {
				logger.Error("Unmarshal error: %v", err)
				return "", errUnmarshal
			}
			break
		}
	}

	score.Score += req.Score

	version := ""
	if len(objects) > 0 {
		version = objects[0].GetVersion()
	}

	value, err := json.Marshal(score)
	if err != nil {
		logger.Error("Marshal error: %v", err)
		return "", errMarshal
	}

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection: "score",
		Key:        "player_score",
		UserID:     userID,
		Value:      string(value),
		Version:    version,		
	}}); err != nil {
		logger.Error("StorageWrite error: %v", err)
		return "", errInternalError
	}	

	resp := addScoreResponse{NewScore: score.Score}
	out, err := json.Marshal(resp)
	if err != nil {
		logger.Error("Marshal error: %v", err)
		return "", errMarshal
	}
	logger.Debug("rpcAddScore: resp: %v", string(out))
	return string(out), nil
}
