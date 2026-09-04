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
	OpCodeMatchOver = 3

	maxPlayers      = 2
	startingHealth  = 100
	cooldownTicks   = 20 // 2 seconds at 10 ticks/sec
	tickRate        = 10
)

type MatchOverPayload struct {
	WinnerID       string `json:"winner_id"`
	WinnerUsername string `json:"winner_username"`
}

type PlayerState struct {
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	Health       int    `json:"health"`
	LastShotTick int64  `json:"-"`
}

type MatchState struct {
	Players map[string]*PlayerState `json:"players"`
	Started bool                    `json:"started"`
	// Set once the match is decided. The match is kept alive for a short
	// grace period afterwards so clients reliably receive OpCodeMatchOver
	// before the handler returns nil and Nakama tears the match down.
	Finished     bool  `json:"finished"`
	FinishedTick int64 `json:"finished_tick"`
}

// Wire format for broadcastState. Unity's JsonUtility can't deserialize a
// map, so the map keyed by user id is flattened to a slice here.
type PlayerStateDto struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Health   int    `json:"health"`
}

type MatchStateDto struct {
	Players []PlayerStateDto `json:"players"`
	Started bool             `json:"started"`
}

type MatchHandlerShooter struct{}

func (m *MatchHandlerShooter) MatchInit(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, params map[string]interface{}) (interface{}, int, string) {
	state := &MatchState{
		Players: make(map[string]*PlayerState),
		Started: false,
	}
	label, _ := json.Marshal(map[string]interface{}{"open": 1, "mode": "shooter"})
	logger.Info("Match initialised")

	return state, tickRate, string(label)
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
			Username:     p.GetUsername(),
			Health:       startingHealth,
			LastShotTick: 0,
		}
		logger.Info("Player joined: %v", p.GetUserId())
	}

	if len(mState.Players) == maxPlayers && !mState.Started {
		mState.Started = true
		newLabel, _ := json.Marshal(map[string]interface{}{"open": 0, "mode": "shooter"})
		dispatcher.MatchLabelUpdate(string(newLabel))
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

	if isMatchOver(mState) && !mState.Finished {
		logger.Info("Match over")
		mState.Finished = true
		mState.FinishedTick = tick
		broadcastMatchOver(logger, dispatcher, mState)
	}

	if mState.Finished && tick-mState.FinishedTick >= 50 {
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
	dto := MatchStateDto{
		Players: make([]PlayerStateDto, 0, len(mState.Players)),
		Started: mState.Started,
	}
	for _, p := range mState.Players {
		dto.Players = append(dto.Players, PlayerStateDto{
			UserID:   p.UserID,
			Username: p.Username,
			Health:   p.Health,
		})
	}

	stateBytes, err := json.Marshal(dto)
	if err != nil {
		logger.Error("Failed to marshal match state: %v", err)
		return
	}
	dispatcher.BroadcastMessage(OpCodeStateSync, stateBytes, nil, nil, true)
}

// The winner is the last player still standing. Both players can be at zero
// health if they forfeit together, in which case the winner fields are left
// empty and clients should treat it as a draw.
func broadcastMatchOver(logger runtime.Logger, dispatcher runtime.MatchDispatcher, mState *MatchState) {
	var payload MatchOverPayload
	for _, p := range mState.Players {
		if p.Health > 0 {
			payload.WinnerID = p.UserID
			payload.WinnerUsername = p.Username
			break
		}
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		logger.Error("Failed to marshal match over payload: %v", err)
		return
	}
	dispatcher.BroadcastMessage(OpCodeMatchOver, payloadBytes, nil, nil, true)
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