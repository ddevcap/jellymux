package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ddevcap/jellyfin-proxy/ent"
	entbackend "github.com/ddevcap/jellyfin-proxy/ent/backend"
	entbackenduser "github.com/ddevcap/jellyfin-proxy/ent/backenduser"
	entuser "github.com/ddevcap/jellyfin-proxy/ent/user"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// backendHTTPTimeout is the timeout for HTTP requests made to backend servers
// during admin operations (login, system info fetch).
const backendHTTPTimeout = 15 * time.Second

// BackendHandler manages backend server registrations and user mappings.
type BackendHandler struct {
	db         *ent.Client
	httpClient *http.Client // shared HTTP client for backend communication
}

func NewBackendHandler(db *ent.Client) *BackendHandler {
	return &BackendHandler{
		db:         db,
		httpClient: &http.Client{Timeout: backendHTTPTimeout},
	}
}

// ── Response shapes ───────────────────────────────────────────────────────────

// backendResponse is the outward representation of a backend server.
// token is intentionally omitted — it is write-only.
type backendResponse struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	ExternalID string    `json:"external_id"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
}

func toBackendResponse(b *ent.Backend) backendResponse {
	return backendResponse{
		ID:         b.ID,
		Name:       b.Name,
		URL:        b.URL,
		ExternalID: b.ExternalID,
		Enabled:    b.Enabled,
		CreatedAt:  b.CreatedAt,
	}
}

// backendUserResponse is the outward representation of a BackendUser mapping.
// backend_token is intentionally omitted — it is write-only.
type backendUserResponse struct {
	ID            uuid.UUID `json:"id"`
	UserID        uuid.UUID `json:"user_id"`
	Username      string    `json:"username"`
	BackendID     uuid.UUID `json:"backend_id"`
	BackendUserID string    `json:"backend_user_id"`
	Enabled       bool      `json:"enabled"`
}

func toBackendUserResponse(bu *ent.BackendUser, backendID uuid.UUID) backendUserResponse {
	r := backendUserResponse{
		ID:            bu.ID,
		BackendID:     backendID,
		BackendUserID: bu.BackendUserID,
		Enabled:       bu.Enabled,
	}
	if bu.Edges.User != nil {
		r.UserID = bu.Edges.User.ID
		r.Username = bu.Edges.User.Username
	}
	return r
}

// ── Backend CRUD ──────────────────────────────────────────────────────────────

type createBackendRequest struct {
	Name string `json:"name"   binding:"required"`
	URL  string `json:"url"    binding:"required,http_url"`
}

// CreateBackend handles POST /proxy/backends.
// It fetches the server ID from the backend's public info endpoint (no
// credentials needed) and persists the backend record. Per-user tokens are
// created later via LoginToBackend.
func (h *BackendHandler) CreateBackend(c *gin.Context) {
	var req createBackendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	baseURL := strings.TrimRight(req.URL, "/")

	// Fetch the Jellyfin server ID from the public info endpoint (no auth required).
	infoReq, err := http.NewRequestWithContext(c.Request.Context(), "GET",
		baseURL+"/system/info/public", nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build system info request"})
		return
	}

	infoResp, err := h.httpClient.Do(infoReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "backend unreachable: " + err.Error()})
		return
	}
	defer func() { _ = infoResp.Body.Close() }()

	if infoResp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("backend system info returned %d", infoResp.StatusCode)})
		return
	}

	var infoResult struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(infoResp.Body).Decode(&infoResult); err != nil || infoResult.ID == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected backend system info response"})
		return
	}

	// Persist the backend. Per-user tokens are created via LoginToBackend.
	b, err := h.db.Backend.Create().
		SetName(req.Name).
		SetURL(req.URL).
		SetExternalID(infoResult.ID).
		Save(c.Request.Context())
	if err != nil {
		if ent.IsConstraintError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "external_id already registered"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create backend"})
		return
	}

	c.JSON(http.StatusCreated, toBackendResponse(b))
}

// ListBackends handles GET /proxy/backends.
func (h *BackendHandler) ListBackends(c *gin.Context) {
	backends, err := h.db.Backend.Query().
		Order(entbackend.ByName()).
		All(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list backends"})
		return
	}

	resp := make([]backendResponse, len(backends))
	for i, b := range backends {
		resp[i] = toBackendResponse(b)
	}
	c.JSON(http.StatusOK, resp)
}

// GetBackend handles GET /proxy/backends/:id.
func (h *BackendHandler) GetBackend(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid backend ID"})
		return
	}

	b, err := h.db.Backend.Get(c.Request.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "backend not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get backend"})
		return
	}

	c.JSON(http.StatusOK, toBackendResponse(b))
}

// updateBackendRequest uses pointer fields for partial updates.
// external_id is not updatable — changing it would invalidate all
// proxy-scoped item IDs already cached by clients.
type updateBackendRequest struct {
	Name    *string `json:"name"    binding:"omitempty,min=1"`
	URL     *string `json:"url"     binding:"omitempty,http_url"`
	Enabled *bool   `json:"enabled"`
}

// UpdateBackend handles PATCH /proxy/backends/:id.
func (h *BackendHandler) UpdateBackend(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid backend ID"})
		return
	}

	var req updateBackendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	upd := h.db.Backend.UpdateOneID(id)

	upd.SetNillableName(req.Name)
	upd.SetNillableURL(req.URL)

	upd.SetNillableEnabled(req.Enabled)

	if req.Name == nil && req.URL == nil && req.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields provided to update"})
		return
	}

	b, err := upd.Save(c.Request.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "backend not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update backend"})
		return
	}

	c.JSON(http.StatusOK, toBackendResponse(b))
}

// DeleteBackend handles DELETE /proxy/backends/:id.
// Cascade-deletes all backend-user mappings first to avoid FK constraint errors.
func (h *BackendHandler) DeleteBackend(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid backend ID"})
		return
	}

	ctx := c.Request.Context()

	// Delete all user mappings for this backend first.
	_, _ = h.db.BackendUser.Delete().
		Where(entbackenduser.HasBackendWith(entbackend.ID(id))).
		Exec(ctx)

	if err := h.db.Backend.DeleteOneID(id).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "backend not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete backend"})
		return
	}

	c.Status(http.StatusNoContent)
}

// ── BackendUser mapping CRUD ──────────────────────────────────────────────────

type createBackendUserRequest struct {
	UserID        uuid.UUID `json:"user_id"         binding:"required"`
	BackendUserID string    `json:"backend_user_id" binding:"required"`
	// backend_token is optional. When absent, authenticated backend requests
	// will be sent without credentials. Use LoginToBackend to obtain a token.
	BackendToken *string `json:"backend_token"`
}

// CreateBackendUser handles POST /proxy/backends/:id/users.
func (h *BackendHandler) CreateBackendUser(c *gin.Context) {
	backendID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid backend ID"})
		return
	}

	var req createBackendUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	bu, err := h.db.BackendUser.Create().
		SetBackendID(backendID).
		SetUserID(req.UserID).
		SetBackendUserID(req.BackendUserID).
		SetNillableBackendToken(req.BackendToken).
		Save(c.Request.Context())
	if err != nil {
		if ent.IsConstraintError(err) {
			errStr := err.Error()
			switch {
			case strings.Contains(errStr, "backend_users_users_backend_users") ||
				strings.Contains(errStr, "unique"):
				c.JSON(http.StatusConflict, gin.H{"error": "user is already mapped to this backend"})
			case strings.Contains(errStr, "foreign key") ||
				strings.Contains(errStr, "violates foreign key"):
				c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "user_id or backend_id does not exist"})
			default:
				c.JSON(http.StatusConflict, gin.H{"error": "constraint violation: " + errStr})
			}
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create backend user"})
		return
	}

	// Reload with user edge for the response.
	bu, err = h.db.BackendUser.Query().
		Where(entbackenduser.ID(bu.ID)).
		WithUser().
		Only(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reload backend user"})
		return
	}

	c.JSON(http.StatusCreated, toBackendUserResponse(bu, backendID))
}

// ListBackendUsers handles GET /proxy/backends/:id/users.
func (h *BackendHandler) ListBackendUsers(c *gin.Context) {
	backendID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid backend ID"})
		return
	}

	mappings, err := h.db.BackendUser.Query().
		Where(entbackenduser.HasBackendWith(entbackend.ID(backendID))).
		WithUser(func(q *ent.UserQuery) {
			q.Order(entuser.ByUsername())
		}).
		All(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list backend users"})
		return
	}

	resp := make([]backendUserResponse, len(mappings))
	for i, bu := range mappings {
		resp[i] = toBackendUserResponse(bu, backendID)
	}
	c.JSON(http.StatusOK, resp)
}

// updateBackendUserRequest uses pointer fields for partial updates.
type updateBackendUserRequest struct {
	BackendUserID *string `json:"backend_user_id" binding:"omitempty,min=1"`
	// Set to "" to clear the per-user token.
	BackendToken *string `json:"backend_token"`
	Enabled      *bool   `json:"enabled"`
}

// UpdateBackendUser handles PATCH /proxy/backends/:id/users/:mappingId.
func (h *BackendHandler) UpdateBackendUser(c *gin.Context) {
	mappingID, err := uuid.Parse(c.Param("mappingId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mapping ID"})
		return
	}

	var req updateBackendUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	upd := h.db.BackendUser.UpdateOneID(mappingID)

	upd.SetNillableBackendUserID(req.BackendUserID)

	if req.BackendToken != nil {
		if *req.BackendToken == "" {
			upd.ClearBackendToken()
		} else {
			upd.SetBackendToken(*req.BackendToken)
		}
	}

	upd.SetNillableEnabled(req.Enabled)

	if req.BackendUserID == nil && req.BackendToken == nil && req.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields provided to update"})
		return
	}

	bu, err := upd.Save(c.Request.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "mapping not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update backend user"})
		return
	}

	backendID, _ := uuid.Parse(c.Param("id"))
	bu, err = h.db.BackendUser.Query().
		Where(entbackenduser.ID(bu.ID)).
		WithUser().
		Only(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reload backend user"})
		return
	}

	c.JSON(http.StatusOK, toBackendUserResponse(bu, backendID))
}

// LoginToBackend handles POST /proxy/backends/:id/login.
// Authenticates a proxy user against the backend Jellyfin server and upserts
// the BackendUser mapping with the resulting backend user ID and token.
func (h *BackendHandler) LoginToBackend(c *gin.Context) {
	backendID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid backend ID"})
		return
	}

	var req struct {
		ProxyUserID string `json:"proxy_user_id" binding:"required,uuid"`
		Username    string `json:"username"      binding:"required"`
		Password    string `json:"password"      binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	proxyUserID, _ := uuid.Parse(req.ProxyUserID)

	b, err := h.db.Backend.Get(c.Request.Context(), backendID)
	if err != nil {
		if ent.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "backend not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get backend"})
		return
	}

	if _, err := h.db.User.Get(c.Request.Context(), proxyUserID); err != nil {
		if ent.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "proxy user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify user"})
		return
	}

	// Authenticate against the backend Jellyfin server.
	authBody, _ := json.Marshal(map[string]string{
		"Username": req.Username,
		"Pw":       req.Password,
	})
	backendURL := strings.TrimRight(b.URL, "/") + "/users/authenticatebyname"
	backendReq, err := http.NewRequestWithContext(c.Request.Context(), "POST", backendURL, bytes.NewReader(authBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build backend request"})
		return
	}
	backendReq.Header.Set("Content-Type", "application/json")
	backendReq.Header.Set("X-Emby-Authorization",
		`MediaBrowser Client="jellyfin-proxy", Device="proxy", DeviceId="jellyfin-proxy-admin", Version="1.0"`)

	client := h.httpClient
	resp, err := client.Do(backendReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "backend unreachable: " + err.Error()})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("backend returned %d", resp.StatusCode)})
		return
	}

	var authResp struct {
		User        struct{ Id string } `json:"User"`
		AccessToken string              `json:"AccessToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil || authResp.User.Id == "" || authResp.AccessToken == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected backend auth response"})
		return
	}

	// Upsert the BackendUser mapping.
	existing, err := h.db.BackendUser.Query().
		Where(
			entbackenduser.HasUserWith(entuser.ID(proxyUserID)),
			entbackenduser.HasBackendWith(entbackend.ID(backendID)),
		).
		Only(c.Request.Context())

	var bu *ent.BackendUser
	status := http.StatusOK
	if err == nil {
		bu, err = h.db.BackendUser.UpdateOneID(existing.ID).
			SetBackendUserID(authResp.User.Id).
			SetBackendToken(authResp.AccessToken).
			Save(c.Request.Context())
	} else if ent.IsNotFound(err) {
		status = http.StatusCreated
		bu, err = h.db.BackendUser.Create().
			SetBackendID(backendID).
			SetUserID(proxyUserID).
			SetBackendUserID(authResp.User.Id).
			SetBackendToken(authResp.AccessToken).
			Save(c.Request.Context())
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save mapping"})
		return
	}

	bu, err = h.db.BackendUser.Query().Where(entbackenduser.ID(bu.ID)).WithUser().Only(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reload backend user"})
		return
	}
	c.JSON(status, toBackendUserResponse(bu, backendID))
}

// DeleteBackendUser handles DELETE /proxy/backends/:id/users/:mappingId.
func (h *BackendHandler) DeleteBackendUser(c *gin.Context) {
	mappingID, err := uuid.Parse(c.Param("mappingId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mapping ID"})
		return
	}

	if err := h.db.BackendUser.DeleteOneID(mappingID).Exec(c.Request.Context()); err != nil {
		if ent.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "mapping not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete backend user"})
		return
	}

	c.Status(http.StatusNoContent)
}
