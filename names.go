package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"

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

	name := randomName()

	err := nk.AccountUpdateId(ctx, userID, name, nil, "", "", "", "", "")
	if err != nil {
		// The pool is small enough that names start colliding after a few
		// hundred accounts, so retrying into it just rolls the same dice.
		// Fall back to a suffix taken from the user id, which is unique by
		// definition and therefore settles this in one more call.
		name = fmt.Sprintf("%s-%s", name, shortID(userID))
		err = nk.AccountUpdateId(ctx, userID, name, nil, "", "", "", "", "")
	}
	if err != nil {
		// A display name is cosmetic. Returning the error here would fail the
		// authentication that already succeeded, so log it and move on.
		logger.Warn("AccountUpdateId error for %v: %v", userID, err)
		return nil
	}

	logger.Info("Assigned username %v to %v", name, userID)
	return nil
}

func randomName() string {
	return fmt.Sprintf("%s%s%d",
		adjectives[rand.Intn(len(adjectives))],
		nouns[rand.Intn(len(nouns))],
		rand.Intn(1000),
	)
}

// shortID returns the leading hex of a user id, which is a UUID, so this is
// enough to tell any two accounts apart.
func shortID(userID string) string {
	id := strings.ReplaceAll(userID, "-", "")
	if len(id) > 8 {
		return id[:8]
	}
	return id
}