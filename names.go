package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

var adjectives = []string{
	"Mindless", "Reckless", "Silent", "Rusty", "Frantic",
	"Grim", "Wandering", "Bitter", "Hollow", "Restless",
}

var nouns = []string{
	"Scout", "Ranger", "Drifter", "Gunner", "Sentry",
	"Nomad", "Raider", "Hunter", "Warden", "Stray",
}

type nameResponse struct {
	Username string `json:"username"`
}

func afterAuthenticateDevice(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session, in *api.AuthenticateDeviceRequest) error {
	if !out.Created {
		return nil
	}

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return errNoUserIdFound
	}

	name := fmt.Sprintf("%s%s%d",
		adjectives[rand.Intn(len(adjectives))],
		nouns[rand.Intn(len(nouns))],
		rand.Intn(100),
	)

	if err := nk.AccountUpdateId(ctx, userID, name, nil, "", "", "", "", ""); err != nil {
		logger.Error("AccountUpdateId error: %v", err)
		return err
	}

	logger.Info("Assigned username %v to %v", name, userID)
	return nil
}