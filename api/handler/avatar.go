package handler

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"

	"github.com/ddevcap/jellymux/ent"
	"github.com/gabriel-vasile/mimetype"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const maxAvatarBytes = 2 << 20 // 2 MiB

// AvatarHandler serves and stores per-user profile pictures in the proxy DB.
type AvatarHandler struct {
	db *ent.Client
}

func NewAvatarHandler(db *ent.Client) *AvatarHandler {
	return &AvatarHandler{db: db}
}

// GetAvatar handles GET /Users/:userId/Images/Primary.
// Public — Jellyfin clients fetch avatars without a session token.
func (h *AvatarHandler) GetAvatar(c *gin.Context) {
	user, ok := h.resolveUser(c)
	if !ok {
		return
	}
	h.serveAvatar(c, user)
}

// UploadAvatar handles POST /Users/:userId/Images/Primary.
// Authenticated — only the user themselves or an admin may upload.
//
// The Jellyfin web UI sends images as a base64-encoded string (optionally
// prefixed with a data URL header like "data:image/png;base64,"). Raw binary
// bodies (e.g. from mobile clients) are also accepted.
func (h *AvatarHandler) UploadAvatar(c *gin.Context) {
	user, ok := h.resolveUser(c)
	if !ok {
		return
	}
	if !h.isSelfOrAdmin(c, user) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	lr := io.LimitReader(c.Request.Body, maxAvatarBytes*2) // base64 is ~4/3 raw size
	raw, err := io.ReadAll(lr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}
	if len(raw) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty body"})
		return
	}

	data, ct, ok := decodeImageBody(raw, c.GetHeader("Content-Type"))
	if !ok {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "body must be an image"})
		return
	}
	if int64(len(data)) > maxAvatarBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "avatar must be <= 2 MiB"})
		return
	}

	_, err = h.db.User.UpdateOneID(user.ID).
		SetAvatar(data).
		SetAvatarContentType(ct).
		Save(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save avatar"})
		return
	}
	c.Status(http.StatusNoContent)
}

// DeleteAvatar handles DELETE /Users/:userId/Images/Primary.
// Authenticated — only the user themselves or an admin may delete.
func (h *AvatarHandler) DeleteAvatar(c *gin.Context) {
	user, ok := h.resolveUser(c)
	if !ok {
		return
	}
	if !h.isSelfOrAdmin(c, user) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	_, err := h.db.User.UpdateOneID(user.ID).
		ClearAvatar().
		ClearAvatarContentType().
		Save(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete avatar"})
		return
	}
	c.Status(http.StatusNoContent)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// decodeImageBody accepts a raw body that may be:
//  1. A data URL:  "data:image/png;base64,<b64data>"
//  2. Raw base64 text (no header) — Jellyfin web UI sends this
//  3. Raw binary bytes
//
// Returns the decoded image bytes, content-type, and whether it was valid.
func decodeImageBody(body []byte, headerCT string) ([]byte, string, bool) {
	s := strings.TrimSpace(string(body))

	// Case 1: data URL.
	if strings.HasPrefix(s, "data:") {
		comma := strings.Index(s, ",")
		if comma == -1 {
			return nil, "", false
		}
		meta := s[5:comma] // everything between "data:" and ","
		payload := s[comma+1:]
		ct := strings.TrimSuffix(strings.SplitN(meta, ";", 2)[0], " ")
		if !strings.HasPrefix(ct, "image/") {
			return nil, "", false
		}
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(payload)
			if err != nil {
				return nil, "", false
			}
		}
		return decoded, ct, true
	}

	// Case 2: raw base64 — all bytes are valid base64 alphabet characters.
	// Jellyfin web UI posts the image as a plain base64 string.
	if looksLikeBase64(s) {
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(s)
			if err != nil {
				// Not valid base64 after all — fall through to binary.
				goto binary
			}
		}
		ct := sniffImageType(decoded, headerCT)
		if ct == "" {
			return nil, "", false
		}
		return decoded, ct, true
	}

binary:
	// Case 3: raw binary.
	ct := sniffImageType(body, headerCT)
	if ct == "" {
		return nil, "", false
	}
	return body, ct, true
}

// sniffImageType returns the content-type of image bytes, preferring the
// provided header value when it is already an image/* type, and falling back
// to mimetype.Detect which recognises more formats than the stdlib (WebP,
// AVIF, HEIC, etc.). Returns "" when neither is an image.
func sniffImageType(data []byte, headerCT string) string {
	mimeType := strings.SplitN(headerCT, ";", 2)[0]
	if strings.HasPrefix(mimeType, "image/") {
		return mimeType
	}
	detected := mimetype.Detect(data)
	if strings.HasPrefix(detected.String(), "image/") {
		return detected.String()
	}
	return ""
}

// looksLikeBase64 returns true when all non-whitespace bytes are in the
// base64 alphabet (A-Z, a-z, 0-9, +, /, =).
func looksLikeBase64(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '+' || c == '/' || c == '=' || c == '\n' || c == '\r':
		default:
			return false
		}
	}
	return true
}

// GetAvatarByQuery handles GET /UserImage?userId=...
// The Android TV app uses this endpoint instead of /Users/:userId/Images/Primary.
func (h *AvatarHandler) GetAvatarByQuery(c *gin.Context) {
	idStr := c.Query("userId")
	if idStr == "" {
		// Fall back to the authenticated user.
		user := userFromCtx(c)
		if user == nil {
			c.Status(http.StatusNotFound)
			return
		}
		h.serveAvatar(c, user)
		return
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}
	user, err := h.db.User.Get(c.Request.Context(), id)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	h.serveAvatar(c, user)
}

// serveAvatar writes the user's avatar image to the response.
func (h *AvatarHandler) serveAvatar(c *gin.Context, user *ent.User) {
	if user.Avatar == nil || len(*user.Avatar) == 0 {
		c.Status(http.StatusNotFound)
		return
	}
	ct := "image/jpeg"
	if user.AvatarContentType != nil && *user.AvatarContentType != "" {
		ct = *user.AvatarContentType
	}
	c.Data(http.StatusOK, ct, *user.Avatar)
}

func (h *AvatarHandler) resolveUser(c *gin.Context) (*ent.User, bool) {
	id, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return nil, false
	}
	user, err := h.db.User.Get(c.Request.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up user"})
		return nil, false
	}
	return user, true
}

// isSelfOrAdmin returns true when the authenticated caller is the target user
// or is an admin.
func (h *AvatarHandler) isSelfOrAdmin(c *gin.Context, target *ent.User) bool {
	caller := userFromCtx(c)
	if caller == nil {
		return false
	}
	return caller.ID == target.ID || caller.IsAdmin
}
