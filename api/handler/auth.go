package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/ddevcap/jellyfin-proxy/api/middleware"
	"github.com/ddevcap/jellyfin-proxy/config"
	"github.com/ddevcap/jellyfin-proxy/ent"
	entsession "github.com/ddevcap/jellyfin-proxy/ent/session"
	entuser "github.com/ddevcap/jellyfin-proxy/ent/user"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const BcryptCost = 12

type AuthHandler struct {
	db             *ent.Client
	cfg            config.Config
	onLoginFail    func(string)
	onLoginSuccess func(string)
}

func NewAuthHandler(db *ent.Client, cfg config.Config, onFail, onSuccess func(string)) *AuthHandler {
	return &AuthHandler{
		db:             db,
		cfg:            cfg,
		onLoginFail:    onFail,
		onLoginSuccess: onSuccess,
	}
}

type authenticateRequest struct {
	Username string `json:"Username" binding:"required"`
	Pw       string `json:"Pw"`
}

// AuthenticateByName handles POST /Users/AuthenticateByName.
func (h *AuthHandler) AuthenticateByName(c *gin.Context) {
	var req authenticateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ip := middleware.ClientIP(c)

	user, err := h.db.User.Query().
		Where(entuser.Username(req.Username)).
		Only(c.Request.Context())
	if err != nil {
		h.onLoginFail(ip)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.HashedPassword), []byte(req.Pw)); err != nil {
		h.onLoginFail(ip)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}

	h.onLoginSuccess(ip)

	// Extract Jellyfin client identity from the Authorization header.
	authParams := middleware.ParseMediaBrowserAuth(c.GetHeader("Authorization"))
	deviceID := fallback(authParams["DeviceId"], "unknown")
	deviceName := fallback(authParams["Device"], "Unknown Device")
	appName := fallback(authParams["Client"], "Unknown")
	appVersion := authParams["Version"]

	token := strings.ReplaceAll(uuid.New().String(), "-", "")
	sess, err := h.db.Session.Create().
		SetToken(token).
		SetDeviceID(deviceID).
		SetDeviceName(deviceName).
		SetAppName(appName).
		SetNillableAppVersion(nilIfEmpty(appVersion)).
		SetUser(user).
		Save(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create session"})
		return
	}

	now := time.Now().UTC()
	userObj := buildUserObject(user, h.cfg)
	userObj["LastLoginDate"] = now
	userObj["LastActivityDate"] = now

	serverID := dashlessID(h.cfg.ServerID)

	c.JSON(http.StatusOK, gin.H{
		"User": userObj,
		"SessionInfo": gin.H{
			"PlayState": gin.H{
				"CanSeek":       false,
				"IsPaused":      false,
				"IsMuted":       false,
				"RepeatMode":    "RepeatNone",
				"PlaybackOrder": "Default",
			},
			"AdditionalUsers": []interface{}{},
			"Capabilities": gin.H{
				"PlayableMediaTypes":           []string{"Audio", "Video"},
				"SupportedCommands":            []string{},
				"SupportsMediaControl":         false,
				"SupportsPersistentIdentifier": true,
			},
			"RemoteEndPoint":           c.ClientIP(),
			"PlayableMediaTypes":       []string{"Audio", "Video"},
			"Id":                       dashlessUUID(sess.ID),
			"UserId":                   dashlessUUID(user.ID),
			"UserName":                 user.Username,
			"Client":                   appName,
			"LastActivityDate":         now,
			"LastPlaybackCheckIn":      "0001-01-01T00:00:00.0000000Z",
			"DeviceName":               deviceName,
			"DeviceId":                 deviceID,
			"ApplicationVersion":       appVersion,
			"IsActive":                 true,
			"SupportsMediaControl":     false,
			"SupportsRemoteControl":    false,
			"NowPlayingQueue":          []interface{}{},
			"NowPlayingQueueFullItems": []interface{}{},
			"HasCustomDeviceName":      false,
			"ServerId":                 serverID,
			"SupportedCommands":        []string{},
		},
		"AccessToken": token,
		"ServerId":    serverID,
	})
}

// UpdatePassword handles POST /Users/:userId/Password.
// A user may change their own password (CurrentPw required).
// An admin may reset any user's password without providing CurrentPw.
func (h *AuthHandler) UpdatePassword(c *gin.Context) {
	caller := userFromCtx(c)
	if caller == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	targetID, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}

	var req struct {
		CurrentPw     string `json:"CurrentPw"`
		NewPw         string `json:"NewPw"`
		ResetPassword bool   `json:"ResetPassword"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Only the user themselves or an admin can change the password.
	isSelf := caller.ID == targetID
	if !isSelf && !caller.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
		return
	}

	target, err := h.db.User.Get(c.Request.Context(), targetID)
	if err != nil {
		if ent.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get user"})
		return
	}

	// Non-admins must verify their current password.
	if !caller.IsAdmin {
		if err := bcrypt.CompareHashAndPassword([]byte(target.HashedPassword), []byte(req.CurrentPw)); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "Current password is incorrect"})
			return
		}
	}

	newPw := req.NewPw
	if req.ResetPassword {
		newPw = ""
	}
	if !req.ResetPassword && len(newPw) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "NewPw must be at least 8 characters"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPw), BcryptCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	if err := h.db.User.UpdateOneID(targetID).SetHashedPassword(string(hash)).Exec(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update password"})
		return
	}

	// Invalidate all sessions for the target user except the caller's current
	// session, so a compromised token cannot survive a password change.
	currentSession, _ := c.Get(middleware.ContextKeySession)
	if cs, ok := currentSession.(*ent.Session); ok {
		_, _ = h.db.Session.Delete().
			Where(
				entsession.HasUserWith(entuser.ID(targetID)),
				entsession.IDNEQ(cs.ID),
			).
			Exec(c.Request.Context())
	} else {
		// No current session (shouldn't happen) — delete all sessions for the user.
		_, _ = h.db.Session.Delete().
			Where(entsession.HasUserWith(entuser.ID(targetID))).
			Exec(c.Request.Context())
	}

	c.Status(http.StatusNoContent)
}

// Logout handles DELETE|POST /Sessions/Logout.
func (h *AuthHandler) Logout(c *gin.Context) {
	raw, exists := c.Get(middleware.ContextKeySession)
	if !exists {
		c.Status(http.StatusNoContent)
		return
	}
	session := raw.(*ent.Session)
	_ = h.db.Session.DeleteOne(session).Exec(c.Request.Context())
	c.Status(http.StatusNoContent)
}
