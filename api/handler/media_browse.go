package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/ddevcap/jellyfin-proxy/backend"
	"github.com/ddevcap/jellyfin-proxy/idtrans"
	"github.com/gin-gonic/gin"
)

// GetSeasons handles GET /Shows/:seriesId/seasons.
func (h *MediaHandler) GetSeasons(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("seriesId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET",
		"/shows/"+backendID+"/seasons", query, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetEpisodes handles GET /Shows/:seriesId/episodes.
func (h *MediaHandler) GetEpisodes(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("seriesId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	query := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET",
		"/shows/"+backendID+"/episodes", query, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// SearchHints handles GET /Search/Hints — aggregates hits across all backends.
func (h *MediaHandler) SearchHints(c *gin.Context) {
	user := userFromCtx(c)
	clients, err := h.pool.AllForUser(c.Request.Context(), user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	type result struct {
		hints []json.RawMessage
	}
	results := make([]result, len(clients))
	var wg sync.WaitGroup
	for i, sc := range clients {
		wg.Add(1)
		go func(i int, sc *backend.ServerClient) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(c.Request.Context(), fanOutTimeout)
			defer cancel()
			q := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
			body, status, err := sc.ProxyJSON(ctx, "GET", "/search/hints", q, nil)
			if err != nil || status != http.StatusOK {
				return
			}
			var resp struct {
				SearchHints []json.RawMessage `json:"SearchHints"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return
			}
			results[i] = result{hints: resp.SearchHints}
		}(i, sc)
	}
	wg.Wait()

	var allHints []json.RawMessage
	for _, r := range results {
		allHints = append(allHints, r.hints...)
	}
	if allHints == nil {
		allHints = []json.RawMessage{}
	}
	c.JSON(http.StatusOK, gin.H{
		"SearchHints":      allHints,
		"TotalRecordCount": len(allHints),
	})
}

// GetArtists handles GET /Artists — aggregates across all backends.
func (h *MediaHandler) GetArtists(c *gin.Context) {
	h.aggregatePagedItems(c, "/artists", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetAlbumArtists handles GET /Artists/AlbumArtists — aggregates across all backends.
func (h *MediaHandler) GetAlbumArtists(c *gin.Context) {
	h.aggregatePagedItems(c, "/artists/AlbumArtists", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetGenres handles GET /Genres — aggregates across all backends.
func (h *MediaHandler) GetGenres(c *gin.Context) {
	h.aggregatePagedItems(c, "/genres", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetMusicGenres handles GET /MusicGenres — aggregates across all backends.
func (h *MediaHandler) GetMusicGenres(c *gin.Context) {
	h.aggregatePagedItems(c, "/musicgenres", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetStudios handles GET /Studios — aggregates across all backends.
func (h *MediaHandler) GetStudios(c *gin.Context) {
	h.aggregatePagedItems(c, "/studios", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetPersons handles GET /Persons — aggregates across all backends.
func (h *MediaHandler) GetPersons(c *gin.Context) {
	h.aggregatePagedItems(c, "/persons", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetChannels handles GET /Channels — aggregates across all backends.
func (h *MediaHandler) GetChannels(c *gin.Context) {
	h.aggregatePagedItems(c, "/channels", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetLiveTvChannels handles GET /LiveTv/Channels — aggregates across all backends.
func (h *MediaHandler) GetLiveTvChannels(c *gin.Context) {
	h.aggregatePagedItems(c, "/livetv/Channels", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetLiveTvPrograms handles GET /LiveTv/Programs — aggregates across all backends.
func (h *MediaHandler) GetLiveTvPrograms(c *gin.Context) {
	h.aggregatePagedItems(c, "/livetv/Programs", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetLiveTvRecommendedPrograms handles GET /LiveTv/Programs/Recommended — aggregates across all backends.
func (h *MediaHandler) GetLiveTvRecommendedPrograms(c *gin.Context) {
	h.aggregatePagedItems(c, "/livetv/Programs/Recommended", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetLiveTvInfo handles GET /LiveTv/Info — returns info from first available backend.
func (h *MediaHandler) GetLiveTvInfo(c *gin.Context) {
	h.proxyFirstBackend(c, "GET", "/livetv/Info", nil)
}

// GetTrailers handles GET /Trailers — aggregates across all backends.
func (h *MediaHandler) GetTrailers(c *gin.Context) {
	h.aggregatePagedItems(c, "/trailers", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetPlaylists handles GET /Playlists — aggregates across all backends.
func (h *MediaHandler) GetPlaylists(c *gin.Context) {
	h.aggregatePagedItems(c, "/items", func(sc *backend.ServerClient) url.Values {
		q := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
		q.Set("IncludeItemTypes", "Playlist")
		return q
	})
}

// GetPlaylistItems handles GET /Playlists/:itemId/Items.
func (h *MediaHandler) GetPlaylistItems(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET", "/playlists/"+backendID+"/items", q, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetCollectionItems handles GET /Collections/:itemId/Items.
func (h *MediaHandler) GetCollectionItems(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET",
		"/collections/"+backendID+"/items", q, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetNextUp handles GET /Shows/NextUp — aggregates across backends.
func (h *MediaHandler) GetNextUp(c *gin.Context) {
	h.aggregatePagedItems(c, "/shows/NextUp", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetUpcomingEpisodes handles GET /Shows/Upcoming — aggregates across backends.
func (h *MediaHandler) GetUpcomingEpisodes(c *gin.Context) {
	h.aggregatePagedItems(c, "/shows/Upcoming", func(sc *backend.ServerClient) url.Values {
		return forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetSimilarItems handles GET /Items/:itemId/similar.
func (h *MediaHandler) GetSimilarItems(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET", "/items/"+backendID+"/similar", q, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetSimilarMovies handles GET /Movies/:itemId/similar.
func (h *MediaHandler) GetSimilarMovies(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET", "/movies/"+backendID+"/similar", q, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetSimilarShows handles GET /Shows/:itemId/similar.
func (h *MediaHandler) GetSimilarShows(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("seriesId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET", "/shows/"+backendID+"/similar", q, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// SyncPlayList handles GET /SyncPlay/List — returns empty list (not supported cross-backend).
func (h *MediaHandler) SyncPlayList(c *gin.Context) {
	c.JSON(http.StatusOK, []interface{}{})
}

// GetSessions handles GET /Sessions — returns empty list (sessions are local to each backend).
func (h *MediaHandler) GetSessions(c *gin.Context) {
	c.JSON(http.StatusOK, []interface{}{})
}

// GetScheduledTasks handles GET /ScheduledTasks — returns empty list.
func (h *MediaHandler) GetScheduledTasks(c *gin.Context) {
	c.JSON(http.StatusOK, []interface{}{})
}

// GetInstalledPlugins handles GET /Plugins — returns empty list.
func (h *MediaHandler) GetInstalledPlugins(c *gin.Context) {
	c.JSON(http.StatusOK, []interface{}{})
}

// GetNotificationsSummary handles GET /Notifications/Summary.
func (h *MediaHandler) GetNotificationsSummary(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"UnreadCount": 0, "MaxUnreadNotificationLevel": nil})
}

// MarkFavorite handles POST /Users/:userId/FavoriteItems/:itemId.
func (h *MediaHandler) MarkFavorite(c *gin.Context) {
	h.userItemAction(c, "POST", "FavoriteItems")
}

// UnmarkFavorite handles DELETE /Users/:userId/FavoriteItems/:itemId.
func (h *MediaHandler) UnmarkFavorite(c *gin.Context) {
	h.userItemAction(c, "DELETE", "FavoriteItems")
}

// MarkPlayed handles POST /Users/:userId/PlayedItems/:itemId.
func (h *MediaHandler) MarkPlayed(c *gin.Context) {
	h.userItemAction(c, "POST", "PlayedItems")
}

// UnmarkPlayed handles DELETE /Users/:userId/PlayedItems/:itemId.
func (h *MediaHandler) UnmarkPlayed(c *gin.Context) {
	h.userItemAction(c, "DELETE", "PlayedItems")
}

// UpdateUserItemRating handles POST /Users/:userId/Items/:itemId/rating.
func (h *MediaHandler) UpdateUserItemRating(c *gin.Context) {
	h.userItemAction(c, "POST", "Items")
}

// userItemAction routes a user↔item interaction to the correct backend.
// For PlayedItems and FavoriteItems actions, it also triggers a background
// cross-backend sync so watch state is propagated to matching items on
// other backends.
func (h *MediaHandler) userItemAction(c *gin.Context, method, collection string) {
	proxyItemID := c.Param("itemId")
	sc, backendID, err := h.routeByID(c, proxyItemID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var path string
	if collection == "Items" {
		// Rating path: /Users/{uid}/Items/{id}/rating
		path = "/users/" + sc.BackendUserID() + "/items/" + backendID + "/rating"
	} else {
		path = "/users/" + sc.BackendUserID() + "/" + collection + "/" + backendID
	}
	q := forwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, _ := io.ReadAll(io.LimitReader(c.Request.Body, maxBodySize))
	respBody, status, err := sc.ProxyJSON(c.Request.Context(), method, path, q, body)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, respBody, status)

	// Fire-and-forget: sync played/favorite state to other backends.
	if (collection == "PlayedItems" || collection == "FavoriteItems") && status >= 200 && status < 300 {
		user := userFromCtx(c)
		if user != nil {
			allClients, err := h.pool.AllForUser(c.Request.Context(), user)
			if err == nil && len(allClients) > 1 {
				serverID, _, _ := idtrans.Decode(proxyItemID)
				go h.syncWatchState(serverID, backendID, sc, method, collection, allClients)
			}
		}
	}
}

// UpdateUserConfiguration handles POST /Users/:userId/Configuration.
// Stores the user's personal preferences (audio/subtitle language, home screen
// order, etc.). The proxy does not persist these — clients re-send them on
// every session start, and playback state lives on the backend servers.
func (h *MediaHandler) UpdateUserConfiguration(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// UpdateUserPolicy handles POST /Users/:userId/Policy.
// Admin-only call to update a user's policy flags. The proxy manages its own
// policy via the /proxy API; we acknowledge and discard to avoid 404 errors.
func (h *MediaHandler) UpdateUserPolicy(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// proxyFirstBackend forwards the request to the first available backend and
// returns its response. Used for endpoints where only one result makes sense.
func (h *MediaHandler) proxyFirstBackend(c *gin.Context, method, path string, body []byte) {
	user := userFromCtx(c)
	clients, err := h.pool.AllForUser(c.Request.Context(), user)
	if err != nil || len(clients) == 0 {
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	q := forwardQuery(c.Request.URL.Query(), clients[0].BackendUserID())
	respBody, status, err := clients[0].ProxyJSON(c.Request.Context(), method, path, q, body)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, respBody, status)
}
