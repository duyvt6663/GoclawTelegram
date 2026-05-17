package skynetworkflows

import (
	"encoding/json"
	"net/http"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *SkynetWorkflowsFeature
}

type ciFailureRequest struct {
	Log         string `json:"log"`
	Text        string `json:"text,omitempty"`
	Repository  string `json:"repository,omitempty"`
	RunURL      string `json:"run_url,omitempty"`
	PullRequest string `json:"pull_request,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Author      string `json:"author,omitempty"`
	TargetRepo  string `json:"target_repo,omitempty"`
	Channel     string `json:"channel,omitempty"`
	ChatID      string `json:"chat_id,omitempty"`
	LocalKey    string `json:"local_key,omitempty"`
	PeerKind    string `json:"peer_kind,omitempty"`
}

type prConflictRequest struct {
	Repository  string `json:"repository,omitempty"`
	PullRequest string `json:"pull_request,omitempty"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
	Branch      string `json:"branch,omitempty"`
	BaseBranch  string `json:"base_branch,omitempty"`
	Commit      string `json:"commit,omitempty"`
	BaseSHA     string `json:"base_sha,omitempty"`
	Author      string `json:"author,omitempty"`
	MergeState  string `json:"merge_state,omitempty"`
	Checks      string `json:"checks,omitempty"`
	TargetRepo  string `json:"target_repo,omitempty"`
	Channel     string `json:"channel,omitempty"`
	ChatID      string `json:"chat_id,omitempty"`
	LocalKey    string `json:"local_key,omitempty"`
	PeerKind    string `json:"peer_kind,omitempty"`
	Force       bool   `json:"force,omitempty"`
}

type mainSyncRequest struct {
	Repository  string `json:"repository,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Before      string `json:"before,omitempty"`
	CompareURL  string `json:"compare_url,omitempty"`
	Pusher      string `json:"pusher,omitempty"`
	TargetRepo  string `json:"target_repo,omitempty"`
	DeployRepo  string `json:"deploy_repo,omitempty"`
	Port        string `json:"port,omitempty"`
	Session     string `json:"session,omitempty"`
	StartWeb    bool   `json:"start_web,omitempty"`
	DirtyPolicy string `json:"dirty_policy,omitempty"`
	Channel     string `json:"channel,omitempty"`
	ChatID      string `json:"chat_id,omitempty"`
	LocalKey    string `json:"local_key,omitempty"`
	PeerKind    string `json:"peer_kind,omitempty"`
	Force       bool   `json:"force,omitempty"`
}

type feedbackRequest struct {
	Feedback      string `json:"feedback"`
	Text          string `json:"text,omitempty"`
	DeploymentURL string `json:"deployment_url,omitempty"`
	Context       string `json:"context,omitempty"`
	TargetRepo    string `json:"target_repo,omitempty"`
	Channel       string `json:"channel,omitempty"`
	ChatID        string `json:"chat_id,omitempty"`
	LocalKey      string `json:"local_key,omitempty"`
	PeerKind      string `json:"peer_kind,omitempty"`
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/skynet-workflows/status", httpapi.RequireAuth(permissions.RoleViewer, h.handleStatus))
	mux.HandleFunc("POST /v1/beta/skynet-workflows/cicd-failure", httpapi.RequireAuth(permissions.RoleOperator, h.handleCIFailure))
	mux.HandleFunc("POST /v1/beta/skynet-workflows/pr-conflict", httpapi.RequireAuth(permissions.RoleOperator, h.handlePRConflict))
	mux.HandleFunc("POST /v1/beta/skynet-workflows/main-sync", httpapi.RequireAuth(permissions.RoleOperator, h.handleMainSync))
	mux.HandleFunc("POST /v1/beta/skynet-workflows/feedback", httpapi.RequireAuth(permissions.RoleOperator, h.handleFeedback))
}

func (h *handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	counts, err := h.feature.store.counts(tenantKeyFromCtx(storepkg.TenantIDFromContext(r.Context())))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	agents, err := h.feature.ensureAgents(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"feature":     featureName,
		"target_repo": h.feature.resolveTargetRepo(r.Context()),
		"deploy_repo": h.feature.configuredDeployRepo(r.Context()),
		"web_port":    h.feature.configuredWebPort(r.Context()),
		"origin":      h.feature.configuredOrigin(r.Context()),
		"agents":      agents,
		"queues":      counts,
	})
}

func (h *handler) handleCIFailure(w http.ResponseWriter, r *http.Request) {
	var req ciFailureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	logText := req.Log
	if logText == "" {
		logText = req.Text
	}
	if logText == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "log is required")
		return
	}
	if req.TargetRepo != "" {
		if err := h.feature.setTargetRepo(r.Context(), req.TargetRepo); err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
			return
		}
	}
	args := map[string]any{
		"repository":   req.Repository,
		"run_url":      req.RunURL,
		"pull_request": req.PullRequest,
		"branch":       req.Branch,
		"commit":       req.Commit,
		"author":       req.Author,
	}
	message := buildCIFailureMessage(h.feature.resolveTargetRepo(r.Context()), logText, args)
	origin := workflowOrigin{
		Channel:  req.Channel,
		ChatID:   req.ChatID,
		LocalKey: req.LocalKey,
		PeerKind: req.PeerKind,
	}
	if err := h.feature.dispatchAgent(r.Context(), agentKeyCIFixer, message, origin); err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, protocol.ErrInternal, err.Error())
		return
	}
	h.feature.publishCIFailureDispatchLog(r.Context(), args, origin)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "dispatched", "agent": agentKeyCIFixer})
}

func (h *handler) handlePRConflict(w http.ResponseWriter, r *http.Request) {
	var req prConflictRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	if req.PullRequest == "" && req.URL == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "pull_request or url is required")
		return
	}
	if req.TargetRepo != "" {
		if err := h.feature.setTargetRepo(r.Context(), req.TargetRepo); err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
			return
		}
	}
	args := map[string]any{
		"repository":   req.Repository,
		"pull_request": req.PullRequest,
		"title":        req.Title,
		"url":          req.URL,
		"branch":       req.Branch,
		"base_branch":  req.BaseBranch,
		"commit":       req.Commit,
		"base_sha":     req.BaseSHA,
		"author":       req.Author,
		"merge_state":  req.MergeState,
		"checks":       req.Checks,
		"force":        req.Force,
	}
	origin := workflowOrigin{
		Channel:  req.Channel,
		ChatID:   req.ChatID,
		LocalKey: req.LocalKey,
		PeerKind: req.PeerKind,
	}
	status, err := h.feature.dispatchPRConflictResolver(r.Context(), args, origin)
	if err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": status, "agent": agentKeyPRConflictResolver})
}

func (h *handler) handleMainSync(w http.ResponseWriter, r *http.Request) {
	var req mainSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	if req.Branch == "" {
		req.Branch = "main"
	}
	if req.TargetRepo != "" {
		if err := h.feature.setTargetRepo(r.Context(), req.TargetRepo); err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
			return
		}
	}
	args := map[string]any{
		"repository":   req.Repository,
		"branch":       req.Branch,
		"commit":       req.Commit,
		"before":       req.Before,
		"compare_url":  req.CompareURL,
		"pusher":       req.Pusher,
		"target_repo":  req.TargetRepo,
		"deploy_repo":  req.DeployRepo,
		"port":         req.Port,
		"session":      req.Session,
		"start_web":    req.StartWeb,
		"dirty_policy": req.DirtyPolicy,
		"force":        req.Force,
	}
	origin := workflowOrigin{
		Channel:  req.Channel,
		ChatID:   req.ChatID,
		LocalKey: req.LocalKey,
		PeerKind: req.PeerKind,
	}
	result, err := h.feature.syncMainDeployment(r.Context(), args, origin)
	if err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, result)
}

func (h *handler) handleFeedback(w http.ResponseWriter, r *http.Request) {
	var req feedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	feedback := req.Feedback
	if feedback == "" {
		feedback = req.Text
	}
	if feedback == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "feedback is required")
		return
	}
	if req.TargetRepo != "" {
		if err := h.feature.setTargetRepo(r.Context(), req.TargetRepo); err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
			return
		}
	}
	message := "[Skynet Feedback Planning]\n\nTarget repository: " + h.feature.resolveTargetRepo(r.Context()) +
		"\nDeployment URL: " + req.DeploymentURL +
		"\n\nUser feedback:\n" + feedback +
		"\n\nAdditional context:\n" + req.Context +
		"\n\nInspect the local deployment or repository enough to turn this feedback into concrete backlog bullets. Then call skynet_backlog with action \"add\". Do not implement in this planning role."
	if err := h.feature.dispatchAgent(r.Context(), agentKeyFeedbackPlanner, message, workflowOrigin{
		Channel:  req.Channel,
		ChatID:   req.ChatID,
		LocalKey: req.LocalKey,
		PeerKind: req.PeerKind,
	}); err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "dispatched", "agent": agentKeyFeedbackPlanner})
}
