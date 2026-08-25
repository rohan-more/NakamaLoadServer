package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/rand"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	moduleName = "shooter"

	OpCodeShoot     = 1
	OpCodeStateSync = 2

	maxPlayers      = 2
	startingHealth  = 100
	cooldownTicks   = 20 // 2 seconds at 10 ticks/sec
	tickRate        = 10
)

type PlayerState struct {
	UserID       string `json:"user_id"`
	Health       int    `json:"health"`
	LastShotTick int64  `json:"-"`
}

type MatchState struct {
	Players map[string]*PlayerState `json:"players"`
	Started bool                    `json:"started"`
}

type MatchHandlerShooter struct{}

func (m *MatchHandlerShooter) MatchInit(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, params map[string]interface{}) (interface{}, int, string) {
	state := &MatchState{
		Players: make(map[string]*PlayerState),
		Started: false,
	}

	logger.Info("Match initialised")

	return state, tickRate, "shooter"
}

func (m *MatchHandlerShooter) MatchJoinAttempt(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, presence runtime.Presence, metadata map[string]string) (interface{}, bool, string) {
	mState, _ := state.(*MatchState)

	if len(mState.Players) >= maxPlayers {
		return mState, false, "match full"
	}

	if _, alreadyIn := mState.Players[presence.GetUserId()]; alreadyIn {
		return mState, false, "already in match"
	}

	return mState, true, ""
}
func (m *MatchHandlerShooter) MatchJoin(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, presences []runtime.Presence) interface{} {
	mState, _ := state.(*MatchState)

	for _, p := range presences {
		mState.Players[p.GetUserId()] = &PlayerState{
			UserID:       p.GetUserId(),
			Health:       startingHealth,
			LastShotTick: 0,
		}
		logger.Info("Player joined: %v", p.GetUserId())
	}

	if len(mState.Players) == maxPlayers && !mState.Started {
		mState.Started = true
		logger.Info("Match started with %v players", len(mState.Players))
	}

	broadcastState(logger, dispatcher, mState)

	return mState
}
func (m *MatchHandlerShooter) MatchLoop(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, messages []runtime.MatchData) interface{} {
	mState, _ := state.(*MatchState)

	if !mState.Started {
		return mState
	}

	stateChanged := false

	for _, msg := range messages {
		if msg.GetOpCode() != OpCodeShoot {
			continue
		}

		shooterID := msg.GetUserId()
		shooter, ok := mState.Players[shooterID]
		if !ok || shooter.Health <= 0 {
			continue
		}

		if tick-shooter.LastShotTick < cooldownTicks {
			continue
		}

		target := pickRandomTarget(mState, shooterID)
		if target == nil {
			continue
		}

		damage := rand.Intn(10) + 1
		target.Health -= damage
		if target.Health < 0 {
			target.Health = 0
		}
		shooter.LastShotTick = tick
		stateChanged = true

		logger.Info("%v hit %v for %v (health now %v)", shooterID, target.UserID, damage, target.Health)
	}

	if stateChanged {
		broadcastState(logger, dispatcher, mState)
	}

	if isMatchOver(mState) {
		logger.Info("Match over")
		return nil
	}

	return mState
}

func pickRandomTarget(mState *MatchState, shooterID string) *PlayerState {
	var targets []*PlayerState
	for _, player := range mState.Players {
		if player.UserID != shooterID && player.Health > 0 {
			targets = append(targets, player)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	return targets[rand.Intn(len(targets))]
}

func isMatchOver(mState *MatchState) bool {
	activePlayers := 0
	for _, player := range mState.Players {
		if player.Health > 0 {
			activePlayers++
		}	
	}
	return activePlayers <= 1
}

func broadcastState(logger runtime.Logger, dispatcher runtime.MatchDispatcher, mState *MatchState) {
	stateBytes, err := json.Marshal(mState)
	if err != nil {
		logger.Error("Failed to marshal match state: %v", err)
		return
	}	
	dispatcher.BroadcastMessage(OpCodeStateSync, stateBytes, nil, nil, true)
}

func (m *MatchHandlerShooter) MatchTerminate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, graceSeconds int) interface{} {
	logger.Info("Match terminating")
	return state
}

func (m *MatchHandlerShooter) MatchSignal(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, data string) (interface{}, string) {
	return state, ""
}

func (m *MatchHandlerShooter) MatchLeave(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, presences []runtime.Presence) interface{} {
	mState, _ := state.(*MatchState)

	for _, p := range presences {
		if player, ok := mState.Players[p.GetUserId()]; ok {
			player.Health = 0
			logger.Info("Player forfeited: %v", p.GetUserId())
		}
	}

	broadcastState(logger, dispatcher, mState)

	if isMatchOver(mState) {
		logger.Info("Match over by forfeit")
	}

	return mState
}