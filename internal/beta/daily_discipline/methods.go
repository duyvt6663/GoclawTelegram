package dailydiscipline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *DailyDisciplineFeature, router *gateway.MethodRouter) {
	router.Register("beta.daily_discipline.list", feature.handleListMethod)
	router.Register("beta.daily_discipline.responses", feature.handleResponsesMethod)
	router.Register("beta.daily_discipline.upsert", feature.handleUpsertMethod)
	router.Register("beta.daily_discipline.submit", feature.handleSubmitMethod)
	router.Register("beta.daily_discipline.run", feature.handleRunMethod)
}

func (f *DailyDisciplineFeature) handleListMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		Key  string `json:"key"`
		Date string `json:"date"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	tenantID := tenantKeyFromCtx(ctx)
	if params.Key != "" {
		cfg, err := f.store.getConfigByKey(tenantID, params.Key)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, err.Error()))
			return
		}
		localDate, err := resolveLocalDate(cfg, params.Date)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
			return
		}
		status, err := f.statusForConfig(cfg, localDate)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
			return
		}
		client.SendResponse(protocol.NewOKResponse(req.ID, status))
		return
	}

	configs, err := f.store.listConfigs(tenantID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	statuses := make([]ConfigStatus, 0, len(configs))
	for i := range configs {
		localDate, err := resolveLocalDate(&configs[i], params.Date)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
			return
		}
		status, err := f.statusForConfig(&configs[i], localDate)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
			return
		}
		statuses = append(statuses, status)
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"configs": statuses}))
}

func (f *DailyDisciplineFeature) handleResponsesMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		Key  string `json:"key"`
		Date string `json:"date"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if params.Key == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "key is required"))
		return
	}

	cfg, err := f.store.getConfigByKey(tenantKeyFromCtx(ctx), params.Key)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, err.Error()))
		return
	}
	localDate, err := resolveLocalDate(cfg, params.Date)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	responses, err := f.responsesForDate(cfg, localDate)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"config":     cfg,
		"local_date": localDate,
		"responses":  responses,
	}))
}

func (f *DailyDisciplineFeature) handleUpsertMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params upsertConfigParams
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	cfg, err := f.upsertConfigForTenant(tenantKeyFromCtx(ctx), params.toConfig())
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"config": cfg}))
}

func (f *DailyDisciplineFeature) handleSubmitMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params submitResponseParams
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if params.Key == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "key is required"))
		return
	}
	cfg, err := f.store.getConfigByKey(tenantKeyFromCtx(ctx), params.Key)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, err.Error()))
		return
	}
	localDate, err := resolveLocalDate(cfg, params.Date)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	identity := params.identity(client.UserID())
	if identity.ID == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "user_id is required"))
		return
	}
	wake, err := params.parseWake()
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	discipline, err := params.parseDiscipline()
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	activity, err := params.parseActivity()
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	response, err := f.submitDetailedResponse(ctx, cfg, localDate, identity, wake, discipline, activity, optionalString(params.Note), "rpc")
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"response": response}))
}

func (f *DailyDisciplineFeature) handleRunMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params struct {
		Key  string `json:"key"`
		Mode string `json:"mode"`
		Date string `json:"date"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if params.Key == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "key is required"))
		return
	}
	cfg, err := f.store.getConfigByKey(tenantKeyFromCtx(ctx), params.Key)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, err.Error()))
		return
	}
	mode := params.Mode
	if mode == "" {
		mode = "survey"
	}
	localDate, err := resolveLocalDate(cfg, params.Date)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}

	switch mode {
	case "survey":
		if err := f.ensureSurveyPosted(ctx, cfg, localDate); err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
			return
		}
	case "summary":
		if err := f.ensureSummaryPosted(ctx, cfg, localDate); err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
			return
		}
	default:
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, fmt.Sprintf("unsupported mode %q", mode)))
		return
	}

	status, err := f.statusForConfig(cfg, localDate)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"mode":       mode,
		"local_date": localDate,
		"status":     status,
	}))
}

func resolveLocalDate(cfg *SurveyConfig, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return cfg.localDate(time.Now().UTC()), nil
	}
	parsed, err := parseISODate(value)
	if err != nil {
		return "", fmt.Errorf("date must use YYYY-MM-DD")
	}
	return parsed.Format("2006-01-02"), nil
}

type upsertConfigParams struct {
	Key                string `json:"key"`
	Name               string `json:"name"`
	Channel            string `json:"channel"`
	ChatID             string `json:"chat_id"`
	ThreadID           *int   `json:"thread_id,omitempty"`
	Timezone           string `json:"timezone"`
	SurveyWindowStart  string `json:"survey_window_start"`
	SurveyWindowEnd    string `json:"survey_window_end"`
	SummaryTime        string `json:"summary_time"`
	TargetWakeTime     string `json:"target_wake_time"`
	WakeQuestion       string `json:"wake_question"`
	DisciplineQuestion string `json:"discipline_question"`
	ActivityQuestion   string `json:"activity_question"`
	NamedResults       *bool  `json:"named_results,omitempty"`
	StreaksEnabled     *bool  `json:"streaks_enabled,omitempty"`
	DMDetailsEnabled   *bool  `json:"dm_details_enabled,omitempty"`
	Enabled            *bool  `json:"enabled,omitempty"`
}

func (p upsertConfigParams) toConfig() SurveyConfig {
	cfg := SurveyConfig{
		Key:                p.Key,
		Name:               p.Name,
		Channel:            p.Channel,
		ChatID:             p.ChatID,
		Timezone:           p.Timezone,
		SurveyWindowStart:  p.SurveyWindowStart,
		SurveyWindowEnd:    p.SurveyWindowEnd,
		SummaryTime:        p.SummaryTime,
		TargetWakeTime:     p.TargetWakeTime,
		WakeQuestion:       p.WakeQuestion,
		DisciplineQuestion: p.DisciplineQuestion,
		ActivityQuestion:   p.ActivityQuestion,
		Enabled:            true,
	}
	if p.ThreadID != nil {
		cfg.ThreadID = *p.ThreadID
	}
	if p.NamedResults != nil {
		cfg.NamedResults = *p.NamedResults
	}
	if p.StreaksEnabled != nil {
		cfg.StreaksEnabled = *p.StreaksEnabled
	}
	if p.DMDetailsEnabled != nil {
		cfg.DMDetailsEnabled = *p.DMDetailsEnabled
	}
	if p.Enabled != nil {
		cfg.Enabled = *p.Enabled
	}
	return cfg
}

type submitResponseParams struct {
	Key        string `json:"key"`
	Date       string `json:"date"`
	UserID     string `json:"user_id"`
	UserLabel  string `json:"user_label"`
	Wake       string `json:"wake"`
	Discipline string `json:"discipline"`
	Activity   string `json:"activity"`
	Note       string `json:"note"`
}

func (p submitResponseParams) identity(fallbackUserID string) userIdentity {
	id := strings.TrimSpace(p.UserID)
	if id == "" {
		id = strings.TrimSpace(fallbackUserID)
	}
	label := strings.TrimSpace(p.UserLabel)
	if label == "" {
		label = id
	}
	return userIdentity{ID: id, Label: label}
}

func (p submitResponseParams) parseWake() (*string, error) {
	if strings.TrimSpace(p.Wake) == "" {
		return nil, nil
	}
	value, ok := normalizeYesNo(p.Wake)
	if !ok {
		return nil, fmt.Errorf("wake must be yes or no")
	}
	return stringPtr(value), nil
}

func (p submitResponseParams) parseDiscipline() (*string, error) {
	if strings.TrimSpace(p.Discipline) == "" {
		return nil, nil
	}
	value, ok := normalizeYesNo(p.Discipline)
	if !ok {
		return nil, fmt.Errorf("discipline must be yes or no")
	}
	return stringPtr(value), nil
}

func (p submitResponseParams) parseActivity() (*string, error) {
	if strings.TrimSpace(p.Activity) == "" {
		return nil, nil
	}
	value, ok := normalizeActivity(p.Activity)
	if !ok {
		return nil, fmt.Errorf("activity must be none, gym, run, or sport")
	}
	return stringPtr(value), nil
}
