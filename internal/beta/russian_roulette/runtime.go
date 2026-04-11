package russianroulette

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type RouletteRoundView struct {
	RouletteRound
	AliveCount               int             `json:"alive_count"`
	EliminatedCount          int             `json:"eliminated_count"`
	NextPlayer               *RoulettePlayer `json:"next_player,omitempty"`
	CooldownRemainingSeconds int             `json:"cooldown_remaining_seconds,omitempty"`
}

type RouletteStatus struct {
	Config      RouletteConfig     `json:"config"`
	Round       *RouletteRoundView `json:"round,omitempty"`
	Players     []RoulettePlayer   `json:"players,omitempty"`
	Leaderboard []RouletteStat     `json:"leaderboard,omitempty"`
}

type RouletteActionResponse struct {
	Message        string         `json:"message"`
	Status         RouletteStatus `json:"status"`
	StickerFileID  string         `json:"sticker_file_id,omitempty"`
	PenaltyApplied bool           `json:"penalty_applied,omitempty"`
	PenaltyNote    string         `json:"penalty_note,omitempty"`
	Warning        string         `json:"warning,omitempty"`
}

type rouletteCommand struct {
	feature *RussianRouletteFeature
}

func (c *rouletteCommand) Command() string { return "/roulette" }

func (c *rouletteCommand) Description() string {
	return "Play Russian roulette in this group"
}

func (c *rouletteCommand) EnabledForChannel(channel *telegramchannel.Channel) bool {
	if c == nil || c.feature == nil || c.feature.store == nil || channel == nil {
		return false
	}
	return c.feature.hasEnabledConfigForChannel(tenantKey(channel.TenantID()), channel.Name())
}

func (c *rouletteCommand) EnabledForContext(_ context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil || c.feature.store == nil || channel == nil {
		return false
	}
	cfg, err := c.feature.lookupCommandConfig(tenantKey(channel.TenantID()), channel.Name(), cmdCtx)
	return err == nil && cfg != nil && cfg.Enabled
}

func (c *rouletteCommand) Handle(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil || c.feature.store == nil {
		return false
	}
	if !cmdCtx.IsGroup {
		cmdCtx.Reply(ctx, "/roulette only works in Telegram group chats.")
		return true
	}

	cfg, err := c.feature.lookupCommandConfig(tenantKey(channel.TenantID()), channel.Name(), cmdCtx)
	if err != nil {
		if errors.Is(err, errRouletteConfigNotFound) {
			return false
		}
		cmdCtx.Reply(ctx, err.Error())
		return true
	}
	if !cfg.Enabled {
		return false
	}

	parts := strings.Fields(cmdCtx.Text)
	if len(parts) <= 1 {
		status, err := c.feature.statusForConfig(cfg, leaderboardDefaultLimit)
		if err != nil {
			cmdCtx.Reply(ctx, err.Error())
			return true
		}
		cmdCtx.Reply(ctx, formatRouletteHelp(status))
		return true
	}

	actor := parsePlayerIdentity(cmdCtx.SenderID, "")
	if actor.ID == "" {
		cmdCtx.Reply(ctx, "Could not resolve your Telegram identity for roulette.")
		return true
	}

	switch strings.ToLower(parts[1]) {
	case "join":
		resp, err := c.feature.joinRound(cfg, actor)
		c.feature.replyForCommand(ctx, cfg, cmdCtx, resp, err)
	case "leave":
		resp, err := c.feature.leaveRound(cfg, actor)
		c.feature.replyForCommand(ctx, cfg, cmdCtx, resp, err)
	case "start":
		override := 0
		if len(parts) > 2 {
			override, err = strconv.Atoi(parts[2])
			if err != nil {
				cmdCtx.Reply(ctx, "Usage: /roulette start [chamber_size]")
				return true
			}
		}
		resp, err := c.feature.startRound(cfg, actor, override)
		c.feature.replyForCommand(ctx, cfg, cmdCtx, resp, err)
	case "pull", "fire", "trigger":
		resp, err := c.feature.pullTrigger(cfg, actor)
		c.feature.replyForCommand(ctx, cfg, cmdCtx, resp, err)
	case "status":
		status, err := c.feature.statusForConfig(cfg, leaderboardDefaultLimit)
		if err != nil {
			cmdCtx.Reply(ctx, err.Error())
			return true
		}
		cmdCtx.Reply(ctx, formatStatusMessage(status))
	case "leaderboard", "lb", "stats":
		stats, err := c.feature.leaderboardForConfig(cfg, leaderboardDefaultLimit)
		if err != nil {
			cmdCtx.Reply(ctx, err.Error())
			return true
		}
		cmdCtx.Reply(ctx, formatLeaderboardMessage(cfg, stats))
	case "help":
		status, err := c.feature.statusForConfig(cfg, leaderboardDefaultLimit)
		if err != nil {
			cmdCtx.Reply(ctx, err.Error())
			return true
		}
		cmdCtx.Reply(ctx, formatRouletteHelp(status))
	default:
		status, err := c.feature.statusForConfig(cfg, leaderboardDefaultLimit)
		if err != nil {
			cmdCtx.Reply(ctx, "Usage: /roulette join|leave|start [chambers]|pull|status|leaderboard")
			return true
		}
		cmdCtx.Reply(ctx, formatRouletteHelp(status))
	}
	return true
}

func (f *RussianRouletteFeature) replyForCommand(ctx context.Context, cfg *RouletteConfig, cmdCtx telegramchannel.DynamicCommandContext, resp RouletteActionResponse, err error) {
	if err != nil {
		cmdCtx.Reply(ctx, err.Error())
		return
	}
	if announceErr := f.announceAction(ctx, &resp); announceErr != nil {
		cmdCtx.Reply(ctx, resp.Message)
		return
	}
	if resp.Warning != "" {
		cmdCtx.Reply(ctx, resp.Warning)
	}
}

func (f *RussianRouletteFeature) lookupCommandConfig(tenantID, channelName string, cmdCtx telegramchannel.DynamicCommandContext) (*RouletteConfig, error) {
	chatID, threadID := parseCompositeChatTarget(cmdCtx.LocalKey)
	if chatID == "" {
		chatID = strings.TrimSpace(cmdCtx.ChatIDStr)
	}
	if threadID == 0 {
		threadID = normalizeThreadID(cmdCtx.MessageThreadID)
	}
	return f.store.getConfigByTarget(tenantID, channelName, chatID, threadID)
}

func (f *RussianRouletteFeature) upsertConfigForTenant(tenantID string, cfg RouletteConfig) (*RouletteConfig, error) {
	cfg.TenantID = tenantID
	return f.store.upsertConfig(&cfg)
}

func (f *RussianRouletteFeature) hasEnabledConfigForChannel(tenantID, channelName string) bool {
	if f == nil || f.store == nil {
		return false
	}
	configs, err := f.store.listConfigs(tenantID)
	if err != nil {
		return false
	}
	channelName = strings.TrimSpace(channelName)
	for _, cfg := range configs {
		if cfg.Enabled && cfg.Channel == channelName {
			return true
		}
	}
	return false
}

func (f *RussianRouletteFeature) resolveToolConfig(ctx context.Context, key string) (*RouletteConfig, error) {
	tenantID := tenantKeyFromCtx(ctx)
	if key != "" {
		return f.store.getConfigByKey(tenantID, key)
	}

	channelName := strings.TrimSpace(tools.ToolChannelFromCtx(ctx))
	chatID, threadID := chatTargetFromToolContext(ctx, "", 0)
	if channelName != "" && chatID != "" {
		cfg, err := f.store.getConfigByTarget(tenantID, channelName, chatID, threadID)
		if err == nil {
			return cfg, nil
		}
		if !errors.Is(err, errRouletteConfigNotFound) {
			return nil, err
		}
	}

	configs, err := f.store.listConfigs(tenantID)
	if err != nil {
		return nil, err
	}
	enabled := make([]RouletteConfig, 0, len(configs))
	for _, cfg := range configs {
		if cfg.Enabled {
			enabled = append(enabled, cfg)
		}
	}
	if len(enabled) == 0 {
		return nil, fmt.Errorf("no roulette configs are available")
	}
	if len(enabled) == 1 {
		return &enabled[0], nil
	}
	return nil, fmt.Errorf("config key is required when multiple roulette configs are available")
}

func ensurePlayableConfig(cfg *RouletteConfig) error {
	if cfg == nil {
		return fmt.Errorf("roulette config is unavailable")
	}
	if !cfg.Enabled {
		return fmt.Errorf("russian roulette is disabled for this chat")
	}
	return nil
}

func (f *RussianRouletteFeature) listStatuses(tenantID string, leaderboardLimit int) ([]RouletteStatus, error) {
	configs, err := f.store.listConfigs(tenantID)
	if err != nil {
		return nil, err
	}
	statuses := make([]RouletteStatus, 0, len(configs))
	for i := range configs {
		status, err := f.statusForConfig(&configs[i], leaderboardLimit)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func (f *RussianRouletteFeature) statusForConfig(cfg *RouletteConfig, leaderboardLimit int) (RouletteStatus, error) {
	status := RouletteStatus{Config: *cfg}
	round, err := f.store.getActiveRoundByConfig(cfg.TenantID, cfg.ID)
	switch {
	case err == nil:
		players, err := f.store.listPlayers(cfg.TenantID, round.ID)
		if err != nil {
			return status, err
		}
		view := buildRoundView(round, players)
		status.Round = &view
		status.Players = players
	case errors.Is(err, errRouletteRoundNotFound):
	default:
		return status, err
	}

	leaderboard, err := f.store.listStats(cfg.TenantID, cfg.ID, leaderboardLimit)
	if err != nil {
		return status, err
	}
	status.Leaderboard = leaderboard
	return status, nil
}

func buildRoundView(round *RouletteRound, players []RoulettePlayer) RouletteRoundView {
	now := time.Now().UTC()
	view := RouletteRoundView{
		RouletteRound:            *round,
		AliveCount:               alivePlayerCount(players),
		EliminatedCount:          len(players) - alivePlayerCount(players),
		CooldownRemainingSeconds: secondsRemaining(round.CooldownUntil, now),
	}
	if next := currentPlayer(players, round.CurrentPlayerOrder); next != nil {
		cp := *next
		view.NextPlayer = &cp
	}
	return view
}

func (f *RussianRouletteFeature) joinRound(cfg *RouletteConfig, actor playerIdentity) (RouletteActionResponse, error) {
	if err := ensurePlayableConfig(cfg); err != nil {
		return RouletteActionResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now().UTC()
	round, err := f.store.getActiveRoundByConfig(cfg.TenantID, cfg.ID)
	if err != nil && !errors.Is(err, errRouletteRoundNotFound) {
		return RouletteActionResponse{}, err
	}

	if errors.Is(err, errRouletteRoundNotFound) {
		round = &RouletteRound{
			ID:          uuid.NewString(),
			TenantID:    cfg.TenantID,
			ConfigID:    cfg.ID,
			Status:      roundStatusLobby,
			ChamberSize: cfg.ChamberSize,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		player := RoulettePlayer{
			ID:        uuid.NewString(),
			TenantID:  cfg.TenantID,
			RoundID:   round.ID,
			UserID:    actor.ID,
			UserLabel: actor.Label,
			SeatOrder: 1,
			IsAlive:   true,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := f.store.withTx(func(tx *sql.Tx) error {
			if err := f.store.createRoundTx(tx, round); err != nil {
				return err
			}
			return f.store.insertPlayerTx(tx, &player)
		}); err != nil {
			return RouletteActionResponse{}, err
		}
		status, err := f.statusForConfig(cfg, leaderboardDefaultLimit)
		if err != nil {
			return RouletteActionResponse{}, err
		}
		return RouletteActionResponse{
			Message: fmt.Sprintf("%s opened a roulette lobby. Need one more brave soul. Use /roulette join.", actor.Label),
			Status:  status,
		}, nil
	}

	if round.Status != roundStatusLobby {
		return RouletteActionResponse{}, fmt.Errorf("a roulette round is already live here. Wait for the next one or check /roulette status")
	}

	players, err := f.store.listPlayers(cfg.TenantID, round.ID)
	if err != nil {
		return RouletteActionResponse{}, err
	}
	for _, player := range players {
		if player.UserID == actor.ID {
			status, statusErr := f.statusForConfig(cfg, leaderboardDefaultLimit)
			if statusErr != nil {
				return RouletteActionResponse{}, statusErr
			}
			return RouletteActionResponse{
				Message: fmt.Sprintf("%s is already in the roulette lobby.", actor.Label),
				Status:  status,
			}, nil
		}
	}

	player := RoulettePlayer{
		ID:        uuid.NewString(),
		TenantID:  cfg.TenantID,
		RoundID:   round.ID,
		UserID:    actor.ID,
		UserLabel: actor.Label,
		SeatOrder: len(players) + 1,
		IsAlive:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	round.UpdatedAt = now
	if err := f.store.withTx(func(tx *sql.Tx) error {
		if err := f.store.insertPlayerTx(tx, &player); err != nil {
			return err
		}
		return f.store.updateRoundTx(tx, round)
	}); err != nil {
		return RouletteActionResponse{}, err
	}

	status, err := f.statusForConfig(cfg, leaderboardDefaultLimit)
	if err != nil {
		return RouletteActionResponse{}, err
	}
	playerCount := len(status.Players)
	message := fmt.Sprintf("%s joined the roulette lobby. %d player(s) ready.", actor.Label, playerCount)
	if playerCount < 2 {
		message += " Need one more."
	} else {
		message += " Use /roulette start [chambers] when ready."
	}
	return RouletteActionResponse{Message: message, Status: status}, nil
}

func (f *RussianRouletteFeature) leaveRound(cfg *RouletteConfig, actor playerIdentity) (RouletteActionResponse, error) {
	if err := ensurePlayableConfig(cfg); err != nil {
		return RouletteActionResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	round, err := f.store.getActiveRoundByConfig(cfg.TenantID, cfg.ID)
	if err != nil {
		if errors.Is(err, errRouletteRoundNotFound) {
			return RouletteActionResponse{}, fmt.Errorf("there is no roulette lobby to leave right now")
		}
		return RouletteActionResponse{}, err
	}
	if round.Status != roundStatusLobby {
		return RouletteActionResponse{}, fmt.Errorf("no chickening out mid-round. Finish the game or wait for the next lobby")
	}

	players, err := f.store.listPlayers(cfg.TenantID, round.ID)
	if err != nil {
		return RouletteActionResponse{}, err
	}
	found := false
	for _, player := range players {
		if player.UserID == actor.ID {
			found = true
			break
		}
	}
	if !found {
		return RouletteActionResponse{}, fmt.Errorf("%s is not in the current roulette lobby", actor.Label)
	}

	now := time.Now().UTC()
	remaining := 0
	for _, player := range players {
		if player.UserID != actor.ID {
			remaining++
		}
	}
	round.UpdatedAt = now
	if remaining == 0 {
		round.Status = roundStatusCancelled
		round.EndedAt = &now
	}
	if err := f.store.withTx(func(tx *sql.Tx) error {
		if err := f.store.deletePlayerTx(tx, cfg.TenantID, round.ID, actor.ID); err != nil {
			return err
		}
		return f.store.updateRoundTx(tx, round)
	}); err != nil {
		return RouletteActionResponse{}, err
	}

	status, err := f.statusForConfig(cfg, leaderboardDefaultLimit)
	if err != nil {
		return RouletteActionResponse{}, err
	}
	message := fmt.Sprintf("%s left the roulette lobby.", actor.Label)
	if remaining == 0 {
		message += " Lobby closed."
	}
	return RouletteActionResponse{Message: message, Status: status}, nil
}

func (f *RussianRouletteFeature) startRound(cfg *RouletteConfig, actor playerIdentity, chamberOverride int) (RouletteActionResponse, error) {
	if err := ensurePlayableConfig(cfg); err != nil {
		return RouletteActionResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	chamberSize := cfg.ChamberSize
	if chamberOverride > 0 {
		if chamberOverride < minChamberSize || chamberOverride > maxChamberSize {
			return RouletteActionResponse{}, fmt.Errorf("chamber size must be between %d and %d", minChamberSize, maxChamberSize)
		}
		chamberSize = chamberOverride
	}

	now := time.Now().UTC()
	round, err := f.store.getActiveRoundByConfig(cfg.TenantID, cfg.ID)
	if err != nil && !errors.Is(err, errRouletteRoundNotFound) {
		return RouletteActionResponse{}, err
	}

	if errors.Is(err, errRouletteRoundNotFound) {
		round = &RouletteRound{
			ID:          uuid.NewString(),
			TenantID:    cfg.TenantID,
			ConfigID:    cfg.ID,
			Status:      roundStatusLobby,
			ChamberSize: chamberSize,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		player := RoulettePlayer{
			ID:        uuid.NewString(),
			TenantID:  cfg.TenantID,
			RoundID:   round.ID,
			UserID:    actor.ID,
			UserLabel: actor.Label,
			SeatOrder: 1,
			IsAlive:   true,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := f.store.withTx(func(tx *sql.Tx) error {
			if err := f.store.createRoundTx(tx, round); err != nil {
				return err
			}
			return f.store.insertPlayerTx(tx, &player)
		}); err != nil {
			return RouletteActionResponse{}, err
		}
		round, err = f.store.getActiveRoundByConfig(cfg.TenantID, cfg.ID)
		if err != nil {
			return RouletteActionResponse{}, err
		}
	}

	if round.Status == roundStatusActive {
		return RouletteActionResponse{}, fmt.Errorf("the roulette is already live. It is someone else's problem now")
	}
	if round.Status != roundStatusLobby {
		return RouletteActionResponse{}, fmt.Errorf("this roulette lobby is no longer startable")
	}

	players, err := f.store.listPlayers(cfg.TenantID, round.ID)
	if err != nil {
		return RouletteActionResponse{}, err
	}
	alreadyJoined := false
	for _, player := range players {
		if player.UserID == actor.ID {
			alreadyJoined = true
			break
		}
	}
	if !alreadyJoined {
		player := RoulettePlayer{
			ID:        uuid.NewString(),
			TenantID:  cfg.TenantID,
			RoundID:   round.ID,
			UserID:    actor.ID,
			UserLabel: actor.Label,
			SeatOrder: len(players) + 1,
			IsAlive:   true,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := f.store.withTx(func(tx *sql.Tx) error {
			return f.store.insertPlayerTx(tx, &player)
		}); err != nil {
			return RouletteActionResponse{}, err
		}
		players = append(players, player)
	}

	if len(players) < 2 {
		round.UpdatedAt = now
		if err := f.store.withTx(func(tx *sql.Tx) error {
			return f.store.updateRoundTx(tx, round)
		}); err != nil {
			return RouletteActionResponse{}, err
		}
		status, err := f.statusForConfig(cfg, leaderboardDefaultLimit)
		if err != nil {
			return RouletteActionResponse{}, err
		}
		return RouletteActionResponse{
			Message: fmt.Sprintf("%s is ready, but roulette needs at least 2 players. Use /roulette join.", actor.Label),
			Status:  status,
		}, nil
	}

	for i := range players {
		players[i].IsAlive = true
		players[i].EliminatedAt = nil
		players[i].SafePulls = 0
		players[i].UpdatedAt = now
	}
	shufflePlayers(players)

	round.Status = roundStatusActive
	round.ChamberSize = chamberSize
	round.BulletPosition = randomChamberPosition(chamberSize)
	round.PullCount = 0
	round.CurrentPlayerOrder = players[0].SeatOrder
	round.StartedByID = actor.ID
	round.StartedByLabel = actor.Label
	round.CooldownUntil = nil
	round.StartedAt = &now
	round.UpdatedAt = now
	round.EndedAt = nil
	round.WinnerUserID = ""
	round.WinnerLabel = ""

	if err := f.store.withTx(func(tx *sql.Tx) error {
		if err := f.store.updateRoundTx(tx, round); err != nil {
			return err
		}
		for i := range players {
			if err := f.store.updatePlayerTx(tx, &players[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return RouletteActionResponse{}, err
	}

	status, err := f.statusForConfig(cfg, leaderboardDefaultLimit)
	if err != nil {
		return RouletteActionResponse{}, err
	}
	nextPlayerLabel := actor.Label
	if status.Round != nil && status.Round.NextPlayer != nil {
		nextPlayerLabel = status.Round.NextPlayer.UserLabel
	}
	return RouletteActionResponse{
		Message: fmt.Sprintf(
			"Roulette is live with %d chambers. Order: %s. %s pulls first with /roulette pull.",
			chamberSize,
			labelList(status.Players, true),
			nextPlayerLabel,
		),
		Status: status,
	}, nil
}

func (f *RussianRouletteFeature) pullTrigger(cfg *RouletteConfig, actor playerIdentity) (RouletteActionResponse, error) {
	if err := ensurePlayableConfig(cfg); err != nil {
		return RouletteActionResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	round, err := f.store.getActiveRoundByConfig(cfg.TenantID, cfg.ID)
	if err != nil {
		if errors.Is(err, errRouletteRoundNotFound) {
			return RouletteActionResponse{}, fmt.Errorf("there is no active roulette round here. Start one with /roulette start")
		}
		return RouletteActionResponse{}, err
	}
	if round.Status != roundStatusActive {
		return RouletteActionResponse{}, fmt.Errorf("the roulette lobby is not live yet. Use /roulette start")
	}

	players, err := f.store.listPlayers(cfg.TenantID, round.ID)
	if err != nil {
		return RouletteActionResponse{}, err
	}
	player := currentPlayer(players, round.CurrentPlayerOrder)
	if player == nil {
		return RouletteActionResponse{}, fmt.Errorf("roulette turn order is corrupted; reset the lobby and try again")
	}
	if player.UserID != actor.ID {
		return RouletteActionResponse{}, fmt.Errorf("it is %s's turn, not yours", player.UserLabel)
	}

	now := time.Now().UTC()
	if wait := secondsRemaining(round.CooldownUntil, now); wait > 0 {
		return RouletteActionResponse{}, fmt.Errorf("cooldown active. Wait %s before the next pull", formatDurationSeconds(wait))
	}

	round.PullCount++
	player.UpdatedAt = now

	if round.PullCount < round.BulletPosition {
		player.SafePulls++
		next := nextAlivePlayer(players, player.SeatOrder)
		if next == nil {
			return RouletteActionResponse{}, fmt.Errorf("roulette turn order has no surviving next player")
		}
		if cfg.TurnCooldownSeconds > 0 {
			until := now.Add(time.Duration(cfg.TurnCooldownSeconds) * time.Second)
			round.CooldownUntil = &until
		} else {
			round.CooldownUntil = nil
		}
		round.CurrentPlayerOrder = next.SeatOrder
		round.UpdatedAt = now
		if err := f.store.withTx(func(tx *sql.Tx) error {
			if err := f.store.updatePlayerTx(tx, player); err != nil {
				return err
			}
			return f.store.updateRoundTx(tx, round)
		}); err != nil {
			return RouletteActionResponse{}, err
		}
		status, err := f.statusForConfig(cfg, leaderboardDefaultLimit)
		if err != nil {
			return RouletteActionResponse{}, err
		}
		message := fmt.Sprintf("Click. %s survives. %s is up next", player.UserLabel, next.UserLabel)
		if cfg.TurnCooldownSeconds > 0 {
			message += fmt.Sprintf(" in %s", formatDurationSeconds(cfg.TurnCooldownSeconds))
		}
		message += "."
		return RouletteActionResponse{
			Message:       message,
			Status:        status,
			StickerFileID: cfg.SafeStickerFileID,
		}, nil
	}

	player.IsAlive = false
	player.EliminatedAt = &now
	player.UpdatedAt = now

	remainingAlive := alivePlayerCount(players) - 1
	if remainingAlive <= 1 {
		var winnerID string
		var winnerLabel string
		for i := range players {
			if players[i].UserID == player.UserID {
				players[i] = *player
				continue
			}
			if players[i].IsAlive {
				winnerID = players[i].UserID
				winnerLabel = players[i].UserLabel
			}
		}
		round.Status = roundStatusCompleted
		round.WinnerUserID = winnerID
		round.WinnerLabel = winnerLabel
		round.CooldownUntil = nil
		round.CurrentPlayerOrder = 0
		round.UpdatedAt = now
		round.EndedAt = &now
		if err := f.store.withTx(func(tx *sql.Tx) error {
			if err := f.store.updatePlayerTx(tx, player); err != nil {
				return err
			}
			if err := f.store.updateRoundTx(tx, round); err != nil {
				return err
			}
			for _, p := range players {
				if p.UserID == player.UserID {
					p = *player
				}
				if err := f.store.upsertStatTx(tx, cfg.TenantID, cfg.ID, p, winnerID, now); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return RouletteActionResponse{}, err
		}
		status, err := f.statusForConfig(cfg, leaderboardDefaultLimit)
		if err != nil {
			return RouletteActionResponse{}, err
		}
		penaltyApplied, penaltyNote := f.applyPenalty(context.Background(), cfg, *player)
		message := fmt.Sprintf("BANG. %s is out. %s wins the roulette and lives to be annoying another day.", player.UserLabel, winnerLabel)
		if penaltyNote != "" {
			message += " " + penaltyNote
		}
		return RouletteActionResponse{
			Message:        message,
			Status:         status,
			StickerFileID:  firstNonEmpty(cfg.WinnerStickerFileID, cfg.EliminatedStickerFileID),
			PenaltyApplied: penaltyApplied,
			PenaltyNote:    penaltyNote,
		}, nil
	}

	for i := range players {
		if players[i].UserID == player.UserID {
			players[i] = *player
			break
		}
	}
	next := nextAlivePlayer(players, player.SeatOrder)
	if next == nil {
		return RouletteActionResponse{}, fmt.Errorf("roulette could not find the next survivor")
	}
	round.PullCount = 0
	round.BulletPosition = randomChamberPosition(round.ChamberSize)
	round.CurrentPlayerOrder = next.SeatOrder
	round.UpdatedAt = now
	if cfg.TurnCooldownSeconds > 0 {
		until := now.Add(time.Duration(cfg.TurnCooldownSeconds) * time.Second)
		round.CooldownUntil = &until
	} else {
		round.CooldownUntil = nil
	}
	if err := f.store.withTx(func(tx *sql.Tx) error {
		if err := f.store.updatePlayerTx(tx, player); err != nil {
			return err
		}
		return f.store.updateRoundTx(tx, round)
	}); err != nil {
		return RouletteActionResponse{}, err
	}

	status, err := f.statusForConfig(cfg, leaderboardDefaultLimit)
	if err != nil {
		return RouletteActionResponse{}, err
	}
	penaltyApplied, penaltyNote := f.applyPenalty(context.Background(), cfg, *player)
	message := fmt.Sprintf("BANG. %s is eliminated. Fresh cylinder loaded. %s is next", player.UserLabel, next.UserLabel)
	if cfg.TurnCooldownSeconds > 0 {
		message += fmt.Sprintf(" in %s", formatDurationSeconds(cfg.TurnCooldownSeconds))
	}
	message += "."
	if penaltyNote != "" {
		message += " " + penaltyNote
	}
	return RouletteActionResponse{
		Message:        message,
		Status:         status,
		StickerFileID:  cfg.EliminatedStickerFileID,
		PenaltyApplied: penaltyApplied,
		PenaltyNote:    penaltyNote,
	}, nil
}

func (f *RussianRouletteFeature) leaderboardForConfig(cfg *RouletteConfig, limit int) ([]RouletteStat, error) {
	return f.store.listStats(cfg.TenantID, cfg.ID, limit)
}

func (f *RussianRouletteFeature) announceAction(ctx context.Context, resp *RouletteActionResponse) error {
	if f == nil || f.msgBus == nil {
		return fmt.Errorf("message bus is unavailable")
	}
	msg := bus.OutboundMessage{
		Channel:  resp.Status.Config.Channel,
		ChatID:   resp.Status.Config.ChatID,
		Content:  resp.Message,
		Metadata: outboundMeta(resp.Status.Config),
	}
	if sticker := strings.TrimSpace(resp.StickerFileID); sticker != "" {
		msg.Media = []bus.MediaAttachment{{
			URL:         telegramFileIDURLPrefix + sticker,
			ContentType: "application/x-telegram-sticker",
		}}
	}
	f.msgBus.PublishOutbound(msg)
	return nil
}

func (f *RussianRouletteFeature) applyPenalty(ctx context.Context, cfg *RouletteConfig, player RoulettePlayer) (bool, string) {
	switch cfg.PenaltyMode {
	case penaltyModeNone:
		return false, ""
	case penaltyModeTag:
		duration := cfg.PenaltyDurationSeconds
		if duration <= 0 {
			duration = defaultPenaltyDurationSecs
		}
		return true, fmt.Sprintf("Penalty: %s wears the title \"%s\" for %s.", player.UserLabel, cfg.PenaltyTag, formatDurationSeconds(duration))
	case penaltyModeMute:
		if f == nil || f.channelMgr == nil {
			return false, "Penalty skipped: channel manager is unavailable."
		}
		channel, ok := f.channelMgr.GetChannel(cfg.Channel)
		if !ok {
			return false, "Penalty skipped: Telegram channel is unavailable."
		}
		moderator, ok := channel.(interface {
			MuteMember(ctx context.Context, chatID int64, userID int64, duration time.Duration) error
		})
		if !ok || player.UserID == "" {
			return false, "Penalty skipped: mute is not supported on this channel."
		}
		chatID, err := strconv.ParseInt(cfg.ChatID, 10, 64)
		if err != nil || player.UserID == "" {
			return false, "Penalty skipped: invalid Telegram identity for mute."
		}
		userID, err := strconv.ParseInt(player.UserID, 10, 64)
		if err != nil {
			return false, "Penalty skipped: player does not have a numeric Telegram ID."
		}
		duration := cfg.PenaltyDurationSeconds
		if duration <= 0 {
			duration = defaultPenaltyDurationSecs
		}
		if err := moderator.MuteMember(ctx, chatID, userID, time.Duration(duration)*time.Second); err != nil {
			return false, fmt.Sprintf("Penalty failed: mute was requested but Telegram rejected it (%v).", err)
		}
		return true, fmt.Sprintf("Penalty: %s is muted for %s.", player.UserLabel, formatDurationSeconds(duration))
	default:
		return false, ""
	}
}

func formatRouletteHelp(status RouletteStatus) string {
	usage := "Commands: /roulette join, /roulette leave, /roulette start [chambers], /roulette pull, /roulette status, /roulette leaderboard"
	return usage + "\n\n" + formatStatusMessage(status)
}

func formatStatusMessage(status RouletteStatus) string {
	if status.Round == nil {
		line := fmt.Sprintf("%s is idle here. Use /roulette join to open a lobby.", status.Config.Name)
		if len(status.Leaderboard) == 0 {
			return line
		}
		return line + "\n\nTop players:\n" + leaderboardLines(status.Leaderboard)
	}

	round := status.Round
	switch round.Status {
	case roundStatusLobby:
		return fmt.Sprintf(
			"%s lobby\nPlayers: %s\nDefault chambers: %d\nTurn cooldown: %s\nUse /roulette join, then /roulette start [chambers].",
			status.Config.Name,
			labelList(status.Players, true),
			status.Config.ChamberSize,
			formatDurationSeconds(status.Config.TurnCooldownSeconds),
		)
	case roundStatusActive:
		nextPlayerLabel := "unknown"
		if round.NextPlayer != nil {
			nextPlayerLabel = round.NextPlayer.UserLabel
		}
		message := fmt.Sprintf(
			"%s is live\nAlive: %s\nOut: %s\nChambers: %d\nPulls since last bang: %d\nNext: %s",
			status.Config.Name,
			labelList(status.Players, true),
			labelList(status.Players, false),
			round.ChamberSize,
			round.PullCount,
			nextPlayerLabel,
		)
		if round.CooldownRemainingSeconds > 0 {
			message += fmt.Sprintf("\nCooldown: %s", formatDurationSeconds(round.CooldownRemainingSeconds))
		}
		return message
	default:
		return fmt.Sprintf("%s is %s.", status.Config.Name, round.Status)
	}
}

func formatLeaderboardMessage(cfg *RouletteConfig, stats []RouletteStat) string {
	if len(stats) == 0 {
		return fmt.Sprintf("No leaderboard entries for %s yet. Somebody has to win first.", cfg.Name)
	}
	return fmt.Sprintf("%s leaderboard\n%s", cfg.Name, leaderboardLines(stats))
}

func leaderboardLines(stats []RouletteStat) string {
	lines := make([]string, 0, len(stats))
	for i, stat := range stats {
		lines = append(lines, fmt.Sprintf(
			"%d. %s - %d win(s), %d loss(es), %d safe pull(s)",
			i+1, stat.UserLabel, stat.Wins, stat.Losses, stat.SafePulls,
		))
	}
	return strings.Join(lines, "\n")
}

type actionParams struct {
	Key         string `json:"key"`
	UserID      string `json:"user_id"`
	UserLabel   string `json:"user_label"`
	ChamberSize int    `json:"chamber_size,omitempty"`
}

func (p actionParams) identity(fallback string) playerIdentity {
	rawID := strings.TrimSpace(p.UserID)
	if rawID == "" {
		rawID = strings.TrimSpace(fallback)
	}
	return parsePlayerIdentity(rawID, p.UserLabel)
}

func decodeActionBody(body io.Reader) (actionParams, error) {
	var params actionParams
	if body == nil {
		return params, nil
	}
	if err := json.NewDecoder(body).Decode(&params); err != nil && !errors.Is(err, io.EOF) {
		return params, err
	}
	return params, nil
}
