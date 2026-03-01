package api

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ddevcap/jellyfin-proxy/api/handler"
	"github.com/ddevcap/jellyfin-proxy/api/middleware"
	"github.com/ddevcap/jellyfin-proxy/backend"
	"github.com/ddevcap/jellyfin-proxy/config"
	"github.com/ddevcap/jellyfin-proxy/ent"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// corsMiddleware returns a gin-contrib/cors middleware configured with the
// proxy's allowed origins. Credentialed origins from ExternalURL + CORSOrigins
// are accepted with credentials. Unknown origins receive a wildcard
// Allow-Origin without credentials so public resources still work.
func corsMiddleware(cfg config.Config) gin.HandlerFunc {
	allowed := buildAllowedOrigins(cfg.ExternalURL)
	for _, o := range cfg.CORSOrigins {
		allowed[strings.ToLower(o)] = true
	}

	return cors.New(cors.Config{
		AllowOriginWithContextFunc: func(c *gin.Context, origin string) bool {
			if !allowed[strings.ToLower(origin)] {
				// Unknown origin — allow without credentials so public
				// resources (images, streams) still work from web players.
				c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
				c.Writer.Header().Del("Access-Control-Allow-Credentials")
			}
			return true
		},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Content-Length", "Accept", "Accept-Encoding", "Authorization", "X-Emby-Token", "X-Emby-Authorization", "X-MediaBrowser-Token", "User-Agent", "X-Requested-With", "Cache-Control", "Pragma"},
		ExposeHeaders:    []string{"Content-Length", "Content-Type", "X-Emby-Token", "X-Emby-Authorization"},
		AllowCredentials: true,
		MaxAge:           24 * time.Hour,
	})
}

// NewRouter builds and returns an http.Handler.
// The handler lowercases every request path before Gin's router sees it,
// so all routes registered in lowercase match regardless of client casing.
func NewRouter(db *ent.Client, cfg config.Config, pool *backend.Pool, wsHub *handler.WSHub) (http.Handler, func()) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), middleware.RequestID(), middleware.RequestLogger(), corsMiddleware(cfg))

	// Build login rate limiter — shared across all /emby, /jellyfin, and bare prefixes.
	loginMW, onFail, onSuccess, stopLimiter := middleware.LoginRateLimiter(cfg)

	authH := handler.NewAuthHandler(db, cfg, onFail, onSuccess)
	systemH := handler.NewSystemHandler(cfg, db, pool)
	mediaH := handler.NewMediaHandler(pool, cfg, db)
	proxyUserH := handler.NewProxyUserHandler(db)
	backendH := handler.NewBackendHandler(db)
	avatarH := handler.NewAvatarHandler(db)

	// Jellyfin clients may prefix all routes with /emby or /jellyfin.
	for _, base := range []string{"", "/emby", "/jellyfin"} {
		registerRoutes(r, base, db, cfg, loginMW, authH, systemH, mediaH, avatarH)
	}

	// Proxy admin API — not prefixed with /emby or /jellyfin.
	admin := r.Group("/proxy")
	admin.Use(middleware.Auth(db, cfg), middleware.AdminOnly())
	{
		admin.POST("/users", proxyUserH.CreateUser)
		admin.GET("/users", proxyUserH.ListUsers)
		admin.GET("/users/:id", proxyUserH.GetProxyUser)
		admin.GET("/users/:id/backends", proxyUserH.GetUserBackends)
		admin.PATCH("/users/:id", proxyUserH.UpdateUser)
		admin.DELETE("/users/:id", proxyUserH.DeleteUser)

		admin.POST("/backends", backendH.CreateBackend)
		admin.GET("/backends", backendH.ListBackends)
		admin.GET("/backends/:id", backendH.GetBackend)
		admin.PATCH("/backends/:id", backendH.UpdateBackend)
		admin.DELETE("/backends/:id", backendH.DeleteBackend)

		admin.POST("/backends/:id/login", backendH.LoginToBackend)

		admin.POST("/backends/:id/users", backendH.CreateBackendUser)
		admin.GET("/backends/:id/users", backendH.ListBackendUsers)
		admin.PATCH("/backends/:id/users/:mappingId", backendH.UpdateBackendUser)
		admin.DELETE("/backends/:id/users/:mappingId", backendH.DeleteBackendUser)

		// Backend health status — shows availability from the health checker.
		admin.GET("/backends/health", func(c *gin.Context) {
			hc := pool.GetHealthChecker()
			if hc == nil {
				c.JSON(http.StatusOK, []interface{}{})
				return
			}
			c.JSON(http.StatusOK, hc.Statuses())
		})
	}

	// WebSocket — requires valid session token via api_key query param.
	r.GET("/socket", middleware.Auth(db, cfg), handler.WebSocketHandler(wsHub))

	// Health probes — unauthenticated, for container orchestrators.
	r.GET("/health", systemH.HealthLive)
	r.GET("/ready", systemH.HealthReady)

	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "endpoint not found"})
	})

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req.URL.Path = strings.ToLower(req.URL.Path)
		r.ServeHTTP(w, req)
	}), stopLimiter
}

func registerRoutes(
	r *gin.Engine,
	base string,
	db *ent.Client,
	cfg config.Config,
	loginMW gin.HandlerFunc,
	authH *handler.AuthHandler,
	systemH *handler.SystemHandler,
	mediaH *handler.MediaHandler,
	avatarH *handler.AvatarHandler,
) {
	// --- Public (no auth required) ---
	pub := r.Group(base)
	{
		pub.POST("/users/authenticatebyname", loginMW, authH.AuthenticateByName)
		pub.GET("/system/info/public", systemH.InfoPublic)
		pub.GET("/users/public", systemH.UsersPublic)
		pub.GET("/branding/configuration", systemH.BrandingConfiguration)
		pub.GET("/branding/css", systemH.BrandingCss)
		pub.GET("/quickconnect/enabled", systemH.QuickConnectEnabled)
		pub.POST("/quickconnect/initiate", systemH.QuickConnectInitiate)
		pub.GET("/playback/bitratetest", systemH.BitrateTest)

		pub.GET("/items/:itemId/images/:imageType", mediaH.GetImage)
		pub.GET("/items/:itemId/images/:imageType/:imageIndex", mediaH.GetImage)

		pub.GET("/videos/:itemId/*subpath", mediaH.VideoSubpath)
		pub.GET("/audio/:itemId/stream", mediaH.StreamAudio)
		pub.GET("/audio/:itemId/stream.:container", mediaH.StreamAudio)
		pub.GET("/audio/:itemId/universal", mediaH.UniversalAudio)
		pub.GET("/items/:itemId/download", mediaH.Download)

		// Avatar — public so Jellyfin clients can fetch without a session token.
		pub.GET("/users/:userId/images/primary", avatarH.GetAvatar)
		pub.GET("/userimage", avatarH.GetAvatarByQuery)
	}

	// --- Authenticated ---
	priv := r.Group(base)
	priv.Use(middleware.Auth(db, cfg))
	{
		// Session
		priv.DELETE("/sessions/logout", authH.Logout)
		priv.POST("/sessions/logout", authH.Logout)
		priv.GET("/sessions", mediaH.GetSessions)
		priv.POST("/sessions/capabilities", systemH.SessionCapabilitiesFull)
		priv.POST("/sessions/capabilities/full", systemH.SessionCapabilitiesFull)
		priv.POST("/sessions/playing", mediaH.ReportPlaybackStart)
		priv.POST("/sessions/playing/progress", mediaH.ReportPlaybackProgress)
		priv.POST("/sessions/playing/stopped", mediaH.ReportPlaybackStopped)

		// System
		priv.GET("/system/info", systemH.Info)
		priv.GET("/system/endpoint", systemH.GetEndpointInfo)
		priv.GET("/system/info/storage", systemH.InfoStorage)
		priv.GET("/system/activitylog/entries", systemH.ActivityLogEntries)
		priv.GET("/system/configuration", systemH.GetConfiguration)
		priv.GET("/system/configuration/network", systemH.GetConfigurationNetwork)
		priv.GET("/system/logs", systemH.GetSystemLogs)
		priv.GET("/system/logs/log", systemH.GetSystemLogFile)
		priv.GET("/packages", systemH.GetPackages)
		priv.GET("/repositories", systemH.GetRepositories)
		priv.GET("/scheduledtasks", mediaH.GetScheduledTasks)
		priv.GET("/plugins", mediaH.GetInstalledPlugins)
		priv.GET("/notifications/summary", mediaH.GetNotificationsSummary)
		priv.GET("/devices", systemH.GetDevices)
		priv.GET("/localization/options", systemH.GetLocalizationOptions)
		priv.GET("/localization/cultures", systemH.GetLocalizationCultures)
		priv.GET("/localization/countries", systemH.GetLocalizationCountries)
		priv.GET("/parentalratings", systemH.GetParentalRatings)

		// Users
		priv.GET("/users", mediaH.GetUsers)
		priv.GET("/users/:userId", mediaH.GetUser)
		priv.GET("/users/:userId/views", mediaH.GetViews)
		priv.GET("/users/:userId/items", mediaH.GetUserItems)
		priv.GET("/users/:userId/items/latest", mediaH.GetLatestItems)
		priv.GET("/users/:userId/items/resume", mediaH.GetResumeItems)
		priv.GET("/users/:userId/items/:itemId", mediaH.GetUserItem)
		priv.GET("/users/:userId/items/:itemId/localtrailers", mediaH.GetLocalTrailers)
		priv.GET("/users/:userId/items/:itemId/intros", mediaH.GetIntros)
		priv.POST("/users/:userId/favoriteitems/:itemId", mediaH.MarkFavorite)
		priv.DELETE("/users/:userId/favoriteitems/:itemId", mediaH.UnmarkFavorite)
		priv.POST("/users/:userId/playeditems/:itemId", mediaH.MarkPlayed)
		priv.DELETE("/users/:userId/playeditems/:itemId", mediaH.UnmarkPlayed)
		priv.POST("/users/:userId/items/:itemId/rating", mediaH.UpdateUserItemRating)
		priv.POST("/users/:userId/password", authH.UpdatePassword)
		priv.POST("/users/:userId/configuration", mediaH.UpdateUserConfiguration)
		priv.POST("/users/:userId/policy", mediaH.UpdateUserPolicy)
		priv.GET("/userviews", mediaH.GetUserViews)

		// User-scoped shortcuts (Android TV app uses these without /:userId/)
		priv.GET("/useritems/resume", mediaH.GetResumeItems)
		priv.GET("/useritems/latest", mediaH.GetLatestItems)

		// Client logging — no-op, just acknowledge.
		priv.POST("/clientlog/document", systemH.ClientLogDocument)

		// Avatar (authenticated upload / delete; GET is public above)
		priv.POST("/users/:userId/images/primary", avatarH.UploadAvatar)
		priv.DELETE("/users/:userId/images/primary", avatarH.DeleteAvatar)

		// Items
		priv.GET("/items", mediaH.GetItems)
		priv.GET("/items/counts", mediaH.GetItemCounts)
		priv.GET("/items/filters", mediaH.GetQueryFilters)
		priv.GET("/items/filters2", mediaH.GetQueryFilters)
		priv.GET("/items/suggestions", mediaH.GetSuggestedItems)
		priv.GET("/items/latest", mediaH.GetLatestItems)
		priv.GET("/items/:itemId", mediaH.GetItem)
		priv.POST("/items/:itemId", mediaH.UpdateItem)
		priv.DELETE("/items/:itemId", mediaH.DeleteItem)
		priv.GET("/items/:itemId/playbackinfo", mediaH.GetPlaybackInfo)
		priv.POST("/items/:itemId/playbackinfo", mediaH.GetPlaybackInfo)
		priv.POST("/items/:itemId/refresh", mediaH.RefreshItem)
		priv.GET("/items/:itemId/children", mediaH.GetItemChildren)
		priv.GET("/items/:itemId/similar", mediaH.GetSimilarItems)
		priv.GET("/items/:itemId/specialfeatures", mediaH.GetSpecialFeatures)
		priv.GET("/items/:itemId/thememedia", mediaH.GetThemeMedia)

		// TV Shows
		priv.GET("/shows/nextup", mediaH.GetNextUp)
		priv.GET("/shows/upcoming", mediaH.GetUpcomingEpisodes)
		priv.GET("/shows/:seriesId/seasons", mediaH.GetSeasons)
		priv.GET("/shows/:seriesId/episodes", mediaH.GetEpisodes)
		priv.GET("/shows/:seriesId/similar", mediaH.GetSimilarShows)

		// Movies
		priv.GET("/movies/:itemId/similar", mediaH.GetSimilarMovies)

		// Artists
		priv.GET("/artists", mediaH.GetArtists)
		priv.GET("/artists/albumartists", mediaH.GetAlbumArtists)

		// Genres / Studios / Persons
		priv.GET("/genres", mediaH.GetGenres)
		priv.GET("/musicgenres", mediaH.GetMusicGenres)
		priv.GET("/studios", mediaH.GetStudios)
		priv.GET("/persons", mediaH.GetPersons)

		// Search
		priv.GET("/search/hints", mediaH.SearchHints)

		// Trailers
		priv.GET("/trailers", mediaH.GetTrailers)

		// Playlists
		priv.GET("/playlists", mediaH.GetPlaylists)
		priv.GET("/playlists/:itemId/items", mediaH.GetPlaylistItems)

		// Collections
		priv.GET("/collections/:itemId/items", mediaH.GetCollectionItems)

		// Audio
		priv.GET("/audio/:itemId/lyrics", mediaH.Lyrics)

		// Channels
		priv.GET("/channels", mediaH.GetChannels)

		// Live TV
		priv.GET("/livetv/channels", mediaH.GetLiveTvChannels)
		priv.GET("/livetv/programs", mediaH.GetLiveTvPrograms)
		priv.GET("/livetv/programs/recommended", mediaH.GetLiveTvRecommendedPrograms)
		priv.GET("/livetv/info", mediaH.GetLiveTvInfo)

		// SyncPlay
		priv.GET("/syncplay/list", mediaH.SyncPlayList)

		// MediaSegments
		priv.GET("/mediasegments/:itemId", mediaH.GetMediaSegments)

		// Display preferences
		priv.GET("/displaypreferences/:id", systemH.DisplayPreferencesGet)
		priv.POST("/displaypreferences/:id", systemH.DisplayPreferencesUpdate)
	}
}

// buildAllowedOrigins returns a set of lower-cased origin strings that are
// allowed to make credentialed cross-origin requests. It derives the origins
// from the configured ExternalURL and also includes its http/https counterpart
// so that both schemes work during development.
func buildAllowedOrigins(externalURL string) map[string]bool {
	origins := make(map[string]bool)
	if externalURL == "" {
		return origins
	}
	parsed, err := url.Parse(externalURL)
	if err != nil {
		origins[strings.ToLower(externalURL)] = true
		return origins
	}
	// Origin = scheme://host (no trailing slash, no path).
	origin := strings.ToLower(parsed.Scheme + "://" + parsed.Host)
	origins[origin] = true
	// Also allow the opposite scheme so http↔https both work.
	switch parsed.Scheme {
	case "https":
		origins["http://"+strings.ToLower(parsed.Host)] = true
	case "http":
		origins["https://"+strings.ToLower(parsed.Host)] = true
	}
	return origins
}
