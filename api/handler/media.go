package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ddevcap/jellyfin-proxy/api/middleware"
	"github.com/ddevcap/jellyfin-proxy/backend"
	"github.com/ddevcap/jellyfin-proxy/config"
	"github.com/ddevcap/jellyfin-proxy/ent"
	entsession "github.com/ddevcap/jellyfin-proxy/ent/session"
	"github.com/ddevcap/jellyfin-proxy/idtrans"
	"github.com/gin-gonic/gin"
	"github.com/jellydator/ttlcache/v3"
)

// fanOutTimeout is the per-backend deadline for fan-out requests during
// aggregation. If a backend doesn't respond within this window it is skipped
// so that one slow or offline server doesn't block the entire response.
const fanOutTimeout = 5 * time.Second

// maxBodySize is the maximum request body size (10 MiB) the proxy accepts
// for JSON write endpoints. Prevents clients from exhausting memory.
const maxBodySize = 10 << 20

// setApiKey sets the ApiKey query parameter to the backend user's token,
// but only when the token is non-empty. This avoids sending "ApiKey=" (empty)
// to backends, which they interpret as an invalid credential and return 401.
func setApiKey(query url.Values, sc *backend.ServerClient) {
	if t := sc.Token(); t != "" {
		query.Set("ApiKey", t)
	}
}

// MediaHandler handles all media-browsing and playback routes.
type MediaHandler struct {
	pool      *backend.Pool
	cfg       config.Config
	db        *ent.Client
	viewCache *ttlcache.Cache[string, []json.RawMessage]
}

func NewMediaHandler(pool *backend.Pool, cfg config.Config, db *ent.Client) *MediaHandler {
	return &MediaHandler{pool: pool, cfg: cfg, db: db, viewCache: newViewCache()}
}

// ── context helpers ───────────────────────────────────────────────────────────

// tryResolveUser attempts to resolve the proxy user from the request's token
// (X-Emby-Token header, api_key query param, etc.) without requiring the Auth
// middleware to have run. Returns nil if no valid session is found.
//
// When a request carries multiple ApiKey query params (e.g. a leaked backend
// token plus the injected proxy token), each candidate is tried until one
// matches a valid session.
func (h *MediaHandler) tryResolveUser(c *gin.Context) *ent.User {
	if u := userFromCtx(c); u != nil {
		return u
	}

	// Collect all candidate tokens: headers first, then every ApiKey/api_key
	// query value.
	candidates := middleware.ExtractAllTokens(c)
	for _, token := range candidates {
		if token == "" {
			continue
		}
		session, err := h.db.Session.Query().
			Where(entsession.Token(token)).
			WithUser().
			Only(c.Request.Context())
		if err == nil {
			return session.Edges.User
		}
	}
	return nil
}

// ── query-param forwarding ────────────────────────────────────────────────────

// forwardQuery translates a client's query params into params safe to send to a
// backend server:
//   - UserId is replaced with the backend user ID
//   - Known single-ID params are decoded (proxy prefix stripped)
//   - "Ids" (comma-separated) has each entry decoded
//   - All other params are forwarded unchanged
func forwardQuery(src url.Values, backendUserID string) url.Values {
	dst := make(url.Values, len(src))
	for k, vals := range src {
		kLower := strings.ToLower(k)
		canonical := canonicalKey(k)
		switch kLower {
		case "userid":
			if backendUserID != "" {
				dst.Set("UserId", backendUserID)
			}
		case "apikey":
			// Never forward client-side ApiKey to backends — the proxy
			// injects the correct backend token where needed.
			continue
		case "parentid", "seasonid", "seriesid", "albumid", "mediasourceid",
			"startitemid", "adjacentto":
			for _, v := range vals {
				_, backendVal, _ := idtrans.Decode(v)
				dst.Add(canonical, backendVal)
			}
		case "ids":
			for _, v := range vals {
				parts := strings.Split(v, ",")
				for i, part := range parts {
					_, backendID, _ := idtrans.Decode(strings.TrimSpace(part))
					parts[i] = backendID
				}
				dst.Add("Ids", strings.Join(parts, ","))
			}
		default:
			// Normalise key casing for all other params too.
			dst[canonical] = vals
		}
	}
	return dst
}

// canonicalKeys maps lower-cased Jellyfin query param names to their
// canonical (properly-cased) form. Update this map when new query parameters
// are encountered in the Jellyfin API surface.
var canonicalKeys = map[string]string{
	"userid":                 "UserId",
	"parentid":               "ParentId",
	"seasonid":               "SeasonId",
	"seriesid":               "SeriesId",
	"albumid":                "AlbumId",
	"mediasourceid":          "MediaSourceId",
	"ids":                    "Ids",
	"startindex":             "StartIndex",
	"limit":                  "Limit",
	"recursive":              "Recursive",
	"sortorder":              "SortOrder",
	"sortby":                 "SortBy",
	"includeitemtypes":       "IncludeItemTypes",
	"excludeitemtypes":       "ExcludeItemTypes",
	"fields":                 "Fields",
	"filters":                "Filters",
	"mediatypes":             "MediaTypes",
	"imagetypelimit":         "ImageTypeLimit",
	"enableimagetypes":       "EnableImageTypes",
	"enabletotalrecordcount": "EnableTotalRecordCount",
	"isplayed":               "IsPlayed",
	"isfavorite":             "IsFavorite",
	"searchterm":             "SearchTerm",
	"namestartswith":         "NameStartsWith",
	"years":                  "Years",
	"genres":                 "Genres",
	"tags":                   "Tags",
	"officialratings":        "OfficialRatings",
	"hastmdbid":              "HasTmdbId",
	"hasimdbid":              "HasImdbId",
	"mincommunityscore":      "MinCommunityRating",
	"mincreaticscore":        "MinCriticRating",
	"adjacentto":             "AdjacentTo",
	"startitemid":            "StartItemId",
	"collapseboxsetitems":    "CollapseBoxSetItems",
	"excludelocationtypes":   "ExcludeLocationTypes",
}

// canonicalKey returns the properly-cased Jellyfin query param name.
func canonicalKey(k string) string {
	if v, ok := canonicalKeys[strings.ToLower(k)]; ok {
		return v
	}
	return k
}

// ── routing helpers ───────────────────────────────────────────────────────────

// routeByID decodes a proxy item ID, resolves the backend server client with
// the authenticated user's credentials, and returns both the client and the
// raw backend item ID.
func (h *MediaHandler) routeByID(c *gin.Context, proxyID string) (*backend.ServerClient, string, error) {
	serverID, backendID, err := idtrans.Decode(proxyID)
	if err != nil {
		return nil, "", err
	}
	sc, err := h.pool.ForUser(c.Request.Context(), serverID, userFromCtx(c))
	if err != nil {
		return nil, "", err
	}
	return sc, backendID, nil
}

// routeByIDPublic is like routeByID but falls back to an unauthenticated
// backend client when no user session is present. Used for public routes
// (streaming, images) where the video player may fetch resources without
// sending auth headers.
func (h *MediaHandler) routeByIDPublic(c *gin.Context, proxyID string) (*backend.ServerClient, string, error) {
	serverID, backendID, err := idtrans.Decode(proxyID)
	if err != nil {
		return nil, "", err
	}
	user := h.tryResolveUser(c)
	var sc *backend.ServerClient
	if user != nil {
		sc, err = h.pool.ForUser(c.Request.Context(), serverID, user)
	} else {
		sc, err = h.pool.ForBackend(c.Request.Context(), serverID)
	}
	if err != nil {
		return nil, "", err
	}
	return sc, backendID, nil
}

// queryParam returns the first value for the given query parameter, matched
// case-insensitively against the request's raw query string.
// This is necessary because Jellyfin clients send params like "ParentId",
// "parentId", and "parentid" interchangeably.
func queryParam(c *gin.Context, name string) string {
	nameLower := strings.ToLower(name)
	for k, vals := range c.Request.URL.Query() {
		if strings.ToLower(k) == nameLower && len(vals) > 0 {
			return vals[0]
		}
	}
	return ""
}

// serverIDFromQuery extracts the backend server ID (or full merged virtual ID)
// from the parentid or ids query param (case-insensitive).
// Returns the full ID string when it is a merged virtual ID.
func serverIDFromQuery(c *gin.Context) string {
	if pid := queryParam(c, "parentid"); pid != "" {
		if _, ok := idtrans.DecodeMerged(pid); ok {
			return pid
		}
		serverID, _, _ := idtrans.Decode(pid)
		return serverID
	}
	if ids := queryParam(c, "ids"); ids != "" {
		first := strings.TrimSpace(strings.SplitN(ids, ",", 2)[0])
		if _, ok := idtrans.DecodeMerged(first); ok {
			return first
		}
		serverID, _, _ := idtrans.Decode(first)
		return serverID
	}
	return ""
}

// ── response helpers ──────────────────────────────────────────────────────────

func writeJSON(c *gin.Context, body []byte, status int) {
	c.Data(status, "application/json", body)
}

func gatewayError(c *gin.Context, err error) {
	c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
}

func emptyPagedList() gin.H {
	return gin.H{"Items": []interface{}{}, "TotalRecordCount": 0, "StartIndex": 0}
}

// redirectStream issues a 302 redirect pointing the client directly at the
// backend URL. Used when the user's direct_stream flag is true — the client fetches bytes
// from the backend over the local network (e.g. Tailscale) instead of having
// them piped through the proxy.
func redirectStream(c *gin.Context, sc *backend.ServerClient, path string, query url.Values) {
	c.Redirect(http.StatusFound, sc.DirectURL(path, query))
}

// rewriteBaseURL replaces backendBaseURL with proxyBaseURL in body.
func rewriteBaseURL(body []byte, backendBaseURL, proxyBaseURL string) []byte {
	backendBaseURL = strings.TrimRight(backendBaseURL, "/")
	proxyBaseURL = strings.TrimRight(proxyBaseURL, "/")
	if backendBaseURL == "" || backendBaseURL == proxyBaseURL {
		return body
	}
	return bytes.ReplaceAll(body, []byte(backendBaseURL), []byte(proxyBaseURL))
}

// toUUIDForm inserts dashes into a 32-char hex ID.
func toUUIDForm(id string) string {
	if len(id) != 32 {
		return id
	}
	if _, err := hex.DecodeString(id); err != nil {
		return id
	}
	return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:32]
}

// rewritePlaybackInfoURLs rewrites TranscodingUrl and DirectStreamUrl in a
// PlaybackInfo response so they:
//   - Point to the proxy (backendBase replaced with proxyBase)
//   - Use the proxy-prefixed item ID instead of the bare backend ID
//   - Have the backend ApiKey stripped
//
// It is careful to only replace the bare backendID in URL/query-string contexts
// (delimited by /, ?, &, =, ") so it doesn't double-prefix JSON Id fields that
// idtrans.RewriteResponse has already encoded.
func rewritePlaybackInfoURLs(body []byte, backendID, proxyID, backendBase, proxyBase string) []byte {
	// 1. Replace backend host with proxy host
	body = rewriteBaseURL(body, backendBase, proxyBase)

	// 2. Replace bare backend item ID in URL path/query contexts only.
	//    Use delimiter-bounded replacement to avoid touching JSON fields.
	urlDelimiters := [][2]string{
		{"/", "/"},
		{"/", "?"},
		{"/", "\""},
		{"=", "&"},
		{"=", "\""},
		{"=", "\\u0026"}, // JSON-escaped &
		{"=", "}"},
	}
	uuidForm := toUUIDForm(backendID)
	for _, d := range urlDelimiters {
		// Plain hex form
		old := d[0] + backendID + d[1]
		repl := d[0] + proxyID + d[1]
		body = bytes.ReplaceAll(body, []byte(old), []byte(repl))
		// UUID-with-dashes form used in HLS paths
		if uuidForm != backendID {
			oldUUID := d[0] + uuidForm + d[1]
			body = bytes.ReplaceAll(body, []byte(oldUUID), []byte(repl))
		}
	}

	// 3. Strip backend ApiKey from all URL query strings in the body.
	body = stripQueryParam(body, "ApiKey")

	return body
}

// findValueEnd returns the index in body where a query-parameter value ends,
// scanning forward from valueStart. End delimiters are &, \u0026, and ".
func findValueEnd(body []byte, valueStart int) int {
	end := len(body)
	for _, sep := range [][]byte{[]byte("&"), []byte(`\u0026`), []byte(`"`)} {
		if i := bytes.Index(body[valueStart:], sep); i != -1 && valueStart+i < end {
			end = valueStart + i
		}
	}
	return end
}

// stripQueryParam removes all occurrences of a URL query parameter from
// JSON-encoded URL strings (handles both & and \u0026 separators, and the
// case where the parameter is the first one after "?").
func stripQueryParam(body []byte, param string) []byte {
	// Remove &Param=value or \u0026Param=value sequences.
	// The value ends at the next & (\u0026), " or end of string.
	for _, sep := range []string{"&", `\u0026`} {
		needle := []byte(sep + param + "=")
		for {
			idx := bytes.Index(body, needle)
			if idx == -1 {
				break
			}
			end := findValueEnd(body, idx+len(needle))
			body = append(body[:idx], body[end:]...)
		}
	}

	// Handle ?Param=value (first query parameter). Replace "?" with the
	// next separator so the remaining params stay valid:
	//   ?Param=val&rest → ?rest    or   ?Param=val" → ?"
	needle := []byte("?" + param + "=")
	for {
		idx := bytes.Index(body, needle)
		if idx == -1 {
			break
		}
		end := findValueEnd(body, idx+len(needle))
		// Keep the "?" and skip to next separator; if the separator is & or
		// \u0026, skip it too so we get ?nextParam=... instead of ?&nextParam=...
		tail := body[end:]
		if bytes.HasPrefix(tail, []byte(`\u0026`)) {
			end += len(`\u0026`)
		} else if bytes.HasPrefix(tail, []byte("&")) {
			end += 1
		}
		body = append(body[:idx+1], body[end:]...) // keep the "?"
	}

	return body
}

// injectProxyToken appends ApiKey=<token> to every TranscodingUrl and
// DirectStreamUrl value in the JSON body. This is necessary because browsers'
// <video> / HLS elements don't send custom HTTP headers, so the only way the
// proxy can identify the user on subsequent streaming requests is through the
// ApiKey query parameter embedded in the URL itself.
func injectProxyToken(body []byte, token string) []byte {
	suffix := []byte(`\u0026ApiKey=` + url.QueryEscape(token))
	for _, field := range []string{`"TranscodingUrl":"`, `"DirectStreamUrl":"`} {
		needle := []byte(field)
		start := 0
		for {
			idx := bytes.Index(body[start:], needle)
			if idx == -1 {
				break
			}
			idx += start
			// Find the closing " of the URL value.
			valueStart := idx + len(needle)
			closing := bytes.IndexByte(body[valueStart:], '"')
			if closing == -1 {
				break
			}
			insertPos := valueStart + closing
			// Insert &ApiKey=<token> just before the closing ".
			body = append(body[:insertPos], append(suffix, body[insertPos:]...)...)
			start = insertPos + len(suffix) + 1
		}
	}
	return body
}

// ── user policy & object ──────────────────────────────────────────────────────

// buildUserPolicy returns the Jellyfin Policy object for a proxy user.
// Centralised so the same policy shape is returned from both the login
// response (AuthenticateByName) and the user-object endpoints.
func buildUserPolicy(isAdmin bool, directStream bool, cfg config.Config) gin.H {
	return gin.H{
		"IsAdministrator":                  isAdmin,
		"IsHidden":                         !isAdmin,
		"EnableCollectionManagement":       false,
		"EnableSubtitleManagement":         false,
		"EnableLyricManagement":            false,
		"IsDisabled":                       false,
		"DirectStream":                     directStream,
		"BlockedTags":                      []string{},
		"AllowedTags":                      []string{},
		"EnableUserPreferenceAccess":       true,
		"AccessSchedules":                  []interface{}{},
		"BlockUnratedItems":                []interface{}{},
		"EnableRemoteControlOfOtherUsers":  isAdmin,
		"EnableSharedDeviceControl":        isAdmin,
		"EnableRemoteAccess":               true,
		"EnableLiveTvManagement":           isAdmin,
		"EnableLiveTvAccess":               true,
		"EnableMediaPlayback":              true,
		"EnableAudioPlaybackTranscoding":   true,
		"EnableVideoPlaybackTranscoding":   true,
		"EnablePlaybackRemuxing":           true,
		"ForceRemoteSourceTranscoding":     false,
		"EnableContentDeletion":            isAdmin,
		"EnableContentDeletionFromFolders": []string{},
		"EnableContentDownloading":         true,
		"EnableSyncTranscoding":            true,
		"EnableMediaConversion":            false,
		"EnabledDevices":                   []string{},
		"EnableAllDevices":                 true,
		"EnabledChannels":                  []string{},
		"EnableAllChannels":                true,
		"EnabledFolders":                   []string{},
		"EnableAllFolders":                 true,
		"InvalidLoginAttemptCount":         0,
		"LoginAttemptsBeforeLockout":       -1,
		"MaxActiveSessions":                0,
		"EnablePublicSharing":              false,
		"BlockedMediaFolders":              []string{},
		"BlockedChannels":                  []string{},
		"RemoteClientBitrateLimit":         cfg.BitrateLimit,
		"AuthenticationProviderId":         "Jellyfin.Server.Implementations.Users.DefaultAuthenticationProvider",
		"PasswordResetProviderId":          "Jellyfin.Server.Implementations.Users.DefaultPasswordResetProvider",
		"SyncPlayAccess":                   "CreateAndJoinGroups",
	}
}

// buildUserObject returns the Jellyfin user object for a proxy user.
func buildUserObject(user *ent.User, cfg config.Config) gin.H {
	obj := gin.H{
		"Name":                      user.Username,
		"ServerName":                cfg.ServerName,
		"ServerId":                  dashlessID(cfg.ServerID),
		"Id":                        dashlessUUID(user.ID),
		"HasPassword":               true,
		"HasConfiguredPassword":     true,
		"HasConfiguredEasyPassword": false,
		"EnableAutoLogin":           false,
		"Configuration": gin.H{
			"PlayDefaultAudioTrack":      true,
			"SubtitleLanguagePreference": "",
			"DisplayMissingEpisodes":     false,
			"GroupedFolders":             []interface{}{},
			"SubtitleMode":               "Default",
			"DisplayCollectionsView":     false,
			"EnableLocalPassword":        false,
			"OrderedViews":               []interface{}{},
			"LatestItemsExcludes":        []interface{}{},
			"MyMediaExcludes":            []interface{}{},
			"HidePlayedInLatest":         true,
			"RememberAudioSelections":    true,
			"RememberSubtitleSelections": true,
			"EnableNextEpisodeAutoPlay":  true,
			// CastReceiverId is the default Chromecast receiver app ID used by
			// Jellyfin clients. Hardcoded to match the official Jellyfin server.
			"CastReceiverId": "F007D354",
		},
		"Policy": buildUserPolicy(user.IsAdmin, user.DirectStream, cfg),
	}
	if user.Avatar != nil && len(*user.Avatar) > 0 {
		sum := sha256.Sum256(*user.Avatar)
		obj["PrimaryImageTag"] = hex.EncodeToString(sum[:])
	}
	return obj
}

// ── collection type mapping ───────────────────────────────────────────────────

// collectionTypeMeta holds the Jellyfin IncludeItemTypes value and display name
// for a given CollectionType string. Centralises the mapping so it is defined
// once instead of duplicated across multiple switch blocks.
type collectionTypeMeta struct {
	itemType    string // value for IncludeItemTypes (e.g. "movie")
	displayName string // human-readable label (e.g. "Movies")
}

var collectionTypes = map[string]collectionTypeMeta{
	"movies":      {itemType: "movie", displayName: "Movies"},
	"tvshows":     {itemType: "series", displayName: "TV Shows"},
	"music":       {itemType: "musicalbum", displayName: "Music"},
	"books":       {itemType: "book", displayName: "Books"},
	"boxsets":     {itemType: "boxset", displayName: "Collections"},
	"musicvideos": {itemType: "musicvideo", displayName: "Music Videos"},
	"photos":      {itemType: "photo", displayName: "Photos"},
	"homevideos":  {itemType: "video", displayName: "Home Videos"},
	"livetv":      {itemType: "liveTvchannel", displayName: "Live TV"},
}

// collectionTypeToItemType maps CollectionType to IncludeItemTypes
// (e.g. "movies" → "movie").
func collectionTypeToItemType(ct string) string {
	if m, ok := collectionTypes[ct]; ok {
		return m.itemType
	}
	return ""
}
