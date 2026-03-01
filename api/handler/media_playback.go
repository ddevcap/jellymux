package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ddevcap/jellyfin-proxy/api/middleware"
	"github.com/ddevcap/jellyfin-proxy/backend"
	"github.com/ddevcap/jellyfin-proxy/idtrans"
	"github.com/gin-gonic/gin"
)

// GetPlaybackInfo handles GET and POST /Items/:itemId/playbackinfo.
// After the standard JSON rewrite, rewrites any URL fields so that stream
// URLs point to the proxy rather than directly to the backend server.
func (h *MediaHandler) GetPlaybackInfo(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	method := c.Request.Method
	var body []byte
	if method == http.MethodPost {
		body, err = io.ReadAll(io.LimitReader(c.Request.Body, maxBodySize))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "could not read body"})
			return
		}
	}

	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	respBody, status, err := sc.ProxyJSON(c.Request.Context(), method,
		"/items/"+backendID+"/playbackinfo", query, body)
	if err != nil {
		gatewayError(c, err)
		return
	}

	// Rewrite all backend URLs in the response to go through the proxy:
	// - replace backend host with proxy ExternalURL
	// - replace bare item IDs (hex + UUID form) with proxy-prefixed IDs
	// - strip backend ApiKey (proxy handles auth)
	proxyID := idtrans.Encode(sc.ExternalID(), backendID)
	respBody = rewritePlaybackInfoURLs(respBody, backendID, proxyID, sc.ServerURL(), h.cfg.ExternalURL)

	// Inject the proxy session token into streaming URLs. Browsers' <video>
	// elements don't send custom headers (X-Emby-Token), so the only way to
	// identify the user on subsequent HLS / stream requests is via the ApiKey
	// query parameter embedded in the URL.
	proxyToken := middleware.ExtractToken(c)
	if proxyToken != "" {
		respBody = injectProxyToken(respBody, proxyToken)
	}

	writeJSON(c, respBody, status)
}

// GetImage handles GET /Items/:itemId/images/:imageType[/:imageIndex].
// A single handler covers both routes; imageIndex is "" when not present.
func (h *MediaHandler) GetImage(c *gin.Context) {
	proxyID := c.Param("itemId")
	serverID, backendID, err := idtrans.Decode(proxyID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Images are served unauthenticated. Use a user-scoped client when a user
	// is present (better token), otherwise fall back to the server service account.
	var sc *backend.ServerClient
	user := h.tryResolveUser(c)
	if user != nil {
		sc, err = h.pool.ForUser(c.Request.Context(), serverID, user)
	} else {
		sc, err = h.pool.ForBackend(c.Request.Context(), serverID)
	}
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	path := "/items/" + backendID + "/images/" + c.Param("imageType")
	if idx := c.Param("imageIndex"); idx != "" {
		path += "/" + idx
	}

	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	if shouldDirectStream(user) {
		redirectStream(c, sc, path, query)
		return
	}
	if err := sc.ProxyStream(c.Request.Context(), "GET", path, query,
		c.Request.Header, c.Writer); err != nil {
		_ = err // headers may already be written; nothing more we can do
	}
}

// StreamVideo handles GET /Videos/:itemId/stream and /Videos/:itemId/stream.:container.
func (h *MediaHandler) StreamVideo(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	path := "/videos/" + backendID + "/stream"
	if container := c.Param("container"); container != "" {
		path += "." + container
	}
	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())

	if shouldDirectStream(userFromCtx(c)) {
		redirectStream(c, sc, path, query)
		return
	}
	if err := sc.ProxyStream(c.Request.Context(), "GET", path, query,
		c.Request.Header, c.Writer); err != nil {
		_ = err
	}
}

// HLSMasterPlaylist handles GET /Videos/:itemId/master.m3u8 and /videos/:itemId/main.m3u8.
// These are the HLS master playlist URLs returned in TranscodingUrl from PlaybackInfo.
func (h *MediaHandler) HLSMasterPlaylist(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	playlist := c.Param("playlist") // "master.m3u8" or "main.m3u8"
	if playlist == "" {
		playlist = "master.m3u8"
	}
	path := "/videos/" + backendID + "/" + playlist
	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	setApiKey(query, sc)

	if shouldDirectStream(userFromCtx(c)) {
		redirectStream(c, sc, path, query)
		return
	}
	if err := sc.ProxyStream(c.Request.Context(), "GET", path, query,
		c.Request.Header, c.Writer); err != nil {
		_ = err
	}
}

// HLSSegment handles GET /Videos/:itemId/:playSessionId/hls1/:segmentId/:segment.
// These are the individual HLS transport stream segments.
func (h *MediaHandler) HLSSegment(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	path := "/videos/" + backendID + c.Param("playSessionId") + "/hls1" +
		c.Param("segmentId") + "/" + c.Param("segment")
	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	setApiKey(query, sc)

	if shouldDirectStream(userFromCtx(c)) {
		redirectStream(c, sc, path, query)
		return
	}
	if err := sc.ProxyStream(c.Request.Context(), "GET", path, query,
		c.Request.Header, c.Writer); err != nil {
		_ = err
	}
}

// StreamAudio handles GET /Audio/:itemId/stream and /Audio/:itemId/stream.:container.
func (h *MediaHandler) StreamAudio(c *gin.Context) {
	sc, backendID, err := h.routeByIDPublic(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	path := "/audio/" + backendID + "/stream"
	if container := c.Param("container"); container != "" {
		path += "." + container
	}
	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())

	if shouldDirectStream(h.tryResolveUser(c)) {
		redirectStream(c, sc, path, query)
		return
	}
	if err := sc.ProxyStream(c.Request.Context(), "GET", path, query,
		c.Request.Header, c.Writer); err != nil {
		_ = err
	}
}

// UniversalAudio handles GET /Audio/:itemId/universal.
func (h *MediaHandler) UniversalAudio(c *gin.Context) {
	sc, backendID, err := h.routeByIDPublic(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())

	if shouldDirectStream(h.tryResolveUser(c)) {
		redirectStream(c, sc, "/audio/"+backendID+"/universal", query)
		return
	}
	if err := sc.ProxyStream(c.Request.Context(), "GET",
		"/audio/"+backendID+"/universal", query, c.Request.Header, c.Writer); err != nil {
		_ = err
	}
}

// VideoSubpath handles all GET /Videos/:itemId/* requests in one wildcard route
// to avoid Gin parameter-name conflicts. Dispatches based on the subpath:
//
//	/stream[.container]              → direct stream
//	/master.m3u8 | /main.m3u8       → HLS master playlist (re-injects ApiKey)
//	/{session}/hls1/{segId}/{file}   → HLS segment (re-injects ApiKey)
//	/{mediaSourceId}/Subtitles/...   → subtitle stream
//	(anything else)                  → generic proxy stream
func (h *MediaHandler) VideoSubpath(c *gin.Context) {
	sc, backendID, err := h.routeByIDPublic(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	subpath := c.Param("subpath") // always starts with "/"
	trimmed := strings.TrimPrefix(subpath, "/")
	parts := strings.Split(trimmed, "/")

	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())

	// isHLSSegment returns true for paths like /hls1/main/0.mp4 or
	// /{sessionId}/hls1/{segmentId}/{file}.
	isHLSSegment := func() bool {
		if len(parts) >= 2 && parts[0] == "hls1" {
			return true
		}
		if len(parts) >= 4 && parts[1] == "hls1" {
			return true
		}
		return false
	}

	// In direct-stream mode, redirect all video sub-requests straight to the
	// backend. The client (on the same network, e.g. Tailscale) fetches bytes
	// directly without the proxy acting as a middleman.
	if shouldDirectStream(h.tryResolveUser(c)) {
		// HLS and segments need the ApiKey in the redirect URL.
		if strings.HasSuffix(parts[0], ".m3u8") || isHLSSegment() {
			setApiKey(query, sc)
		}
		// Decode mediaSourceId prefix for subtitle paths.
		if len(parts) >= 4 && strings.EqualFold(parts[1], "subtitles") {
			_, msBackendID, _ := idtrans.Decode(parts[0])
			path := "/videos/" + backendID + "/" + msBackendID + "/" + strings.Join(parts[1:], "/")
			redirectStream(c, sc, path, query)
			return
		}
		redirectStream(c, sc, "/videos/"+backendID+subpath, query)
		return
	}

	// Extract the proxy session token from the incoming request so we can
	// inject it into HLS playlist URLs. The browser doesn't send custom
	// headers on <video> sub-requests, so every URL in the playlist must
	// carry the token as a query parameter.
	proxyToken := middleware.ExtractToken(c)

	switch {
	// Direct stream: /stream or /stream.mkv etc.
	case parts[0] == "stream" || strings.HasPrefix(parts[0], "stream."):
		path := "/videos/" + backendID + subpath
		_ = sc.ProxyStream(c.Request.Context(), "GET", path, query, c.Request.Header, c.Writer)

	// HLS master/variant playlist — buffer, rewrite backend URLs, then send.
	case parts[0] == "master.m3u8" || parts[0] == "main.m3u8" ||
		strings.HasSuffix(parts[0], ".m3u8"):
		setApiKey(query, sc)
		path := "/videos/" + backendID + subpath
		body, status, err := sc.ProxyRaw(c.Request.Context(), "GET", path, query)
		if err != nil {
			gatewayError(c, err)
			return
		}
		// Rewrite any absolute backend URLs in the playlist to the proxy URL.
		body = rewriteBaseURL(body, sc.ServerURL(), h.cfg.ExternalURL)
		// Inject the proxy token into every URL in the playlist so that
		// follow-up requests (main.m3u8, segments) can be authenticated.
		if proxyToken != "" {
			body = injectTokenIntoHLSPlaylist(body, proxyToken)
		}
		c.Data(status, "application/vnd.apple.mpegurl", body)

	// HLS segment: /hls1/{segmentId}/{file} or /{session}/hls1/{segmentId}/{file}
	case isHLSSegment():
		setApiKey(query, sc)
		path := "/videos/" + backendID + subpath
		_ = sc.ProxyStream(c.Request.Context(), "GET", path, query, c.Request.Header, c.Writer)

	// Subtitle stream: /{mediaSourceId}/Subtitles/{index}/stream.{format}
	case len(parts) >= 4 && strings.EqualFold(parts[1], "subtitles"):
		_, msBackendID, _ := idtrans.Decode(parts[0])
		path := "/videos/" + backendID + "/" + msBackendID + "/" + strings.Join(parts[1:], "/")
		_ = sc.ProxyStream(c.Request.Context(), "GET", path, query, c.Request.Header, c.Writer)

	// Fallback: proxy as-is
	default:
		path := "/videos/" + backendID + subpath
		_ = sc.ProxyStream(c.Request.Context(), "GET", path, query, c.Request.Header, c.Writer)
	}
}

// GetSubtitle is kept for any direct calls but routes through VideoSubpath logic.
func (h *MediaHandler) GetSubtitle(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, msBackendID, _ := idtrans.Decode(c.Param("mediaSourceId"))
	path := "/videos/" + backendID + "/" + msBackendID +
		"/subtitles/" + c.Param("index") + "/stream." + c.Param("format")
	q := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	_ = sc.ProxyStream(c.Request.Context(), "GET", path, q, c.Request.Header, c.Writer)
}

// injectTokenIntoHLSPlaylist appends &ApiKey=<token> (or ?ApiKey=<token>) to
// every URL line in an HLS playlist. Non-comment, non-empty lines that are not
// #EXT tags are treated as URLs. Any existing ApiKey param (from the backend)
// is stripped first to avoid duplicate/conflicting tokens.
func injectTokenIntoHLSPlaylist(body []byte, token string) []byte {
	lines := strings.Split(string(body), "\n")
	param := "ApiKey=" + url.QueryEscape(token)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			// Also handle #EXT-X-MAP:URI="..." and similar tags with embedded URIs.
			if strings.Contains(trimmed, "URI=\"") {
				lines[i] = injectTokenIntoTagURI(line, param)
			}
			continue
		}
		// Strip any existing ApiKey from the URL (backend token leak).
		line = stripApiKeyFromURL(line)
		// Append the proxy session token.
		if strings.Contains(line, "?") {
			lines[i] = line + "&" + param
		} else {
			lines[i] = line + "?" + param
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// stripApiKeyFromURL removes ApiKey=... from a URL string, handling both
// &ApiKey=value and ?ApiKey=value positions.
func stripApiKeyFromURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	q := parsed.Query()
	q.Del("ApiKey")
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// injectTokenIntoTagURI handles #EXT-X-MAP:URI="init.mp4?query" style tags.
func injectTokenIntoTagURI(line, param string) string {
	const marker = "URI=\""
	idx := strings.Index(line, marker)
	if idx == -1 {
		return line
	}
	uriStart := idx + len(marker)
	closeQuote := strings.IndexByte(line[uriStart:], '"')
	if closeQuote == -1 {
		return line
	}
	uri := line[uriStart : uriStart+closeQuote]
	sep := "?"
	if strings.Contains(uri, "?") {
		sep = "&"
	}
	return line[:uriStart] + uri + sep + param + line[uriStart+closeQuote:]
}

// Download handles GET /Items/:itemId/Download.
// Public endpoint — clients pass their token via the api_key query param.
func (h *MediaHandler) Download(c *gin.Context) {
	sc, backendID, err := h.routeByIDPublic(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	path := "/items/" + backendID + "/download"
	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	if shouldDirectStream(h.tryResolveUser(c)) {
		redirectStream(c, sc, path, query)
		return
	}
	if err := sc.ProxyStream(c.Request.Context(), "GET", path, query,
		c.Request.Header, c.Writer); err != nil {
		_ = err
	}
}

// Lyrics handles GET /Audio/:itemId/Lyrics.
func (h *MediaHandler) Lyrics(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET",
		"/audio/"+backendID+"/lyrics", query, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// ReportPlaybackStart handles POST /Sessions/Playing.
func (h *MediaHandler) ReportPlaybackStart(c *gin.Context) {
	h.forwardPlaybackReport(c, "Playing")
}

// ReportPlaybackProgress handles POST /Sessions/Playing/Progress.
func (h *MediaHandler) ReportPlaybackProgress(c *gin.Context) {
	h.forwardPlaybackReport(c, "Playing/Progress")
}

// ReportPlaybackStopped handles POST /Sessions/Playing/Stopped.
func (h *MediaHandler) ReportPlaybackStopped(c *gin.Context) {
	h.forwardPlaybackReport(c, "Playing/Stopped")
}

// forwardPlaybackReport reads the request body, extracts ItemId to determine
// which backend to route to, and forwards the report.
func (h *MediaHandler) forwardPlaybackReport(c *gin.Context, endpoint string) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodySize))
	if err != nil || len(body) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	var payload struct {
		ItemId string `json:"ItemId"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.ItemId == "" {
		c.Status(http.StatusNoContent)
		return
	}

	serverID, _, err := idtrans.Decode(payload.ItemId)
	if err != nil {
		c.Status(http.StatusNoContent)
		return
	}

	sc, err := h.pool.ForUser(c.Request.Context(), serverID, userFromCtx(c))
	if err != nil {
		c.Status(http.StatusNoContent)
		return
	}

	// ProxyJSON calls RewriteRequest internally to strip proxy prefixes from the body.
	_, status, err := sc.ProxyJSON(c.Request.Context(), "POST",
		"/sessions/"+endpoint, nil, body)
	if err != nil {
		gatewayError(c, err)
		return
	}
	c.Status(status)
}
