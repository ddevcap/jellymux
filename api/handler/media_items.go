package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"

	"github.com/ddevcap/jellymux/backend"
	"github.com/ddevcap/jellymux/idtrans"
	"github.com/gin-gonic/gin"
)

// GetItems handles GET /Items.
// Routes by ParentId or first entry in Ids; fans out to all backends when a
// SearchTerm is present without a ParentId; returns empty list otherwise.
// When ParentId is a merged virtual ID, fans out to all backends.
func (h *MediaHandler) GetItems(c *gin.Context) {
	serverID := ServerIDFromQuery(c)
	if serverID == "" {
		// No parentId/ids — fan out if this is a search request.
		if queryParam(c, "searchterm") == "" {
			c.JSON(http.StatusOK, emptyPagedList())
			return
		}
		h.aggregatePagedItems(c, "/items", func(sc *backend.ServerClient) url.Values {
			return ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
		})
		return
	}

	if collectionType, ok := idtrans.DecodeMerged(serverID); ok {
		h.aggregatePagedItems(c, "/items", func(sc *backend.ServerClient) url.Values {
			q := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
			q.Del("ParentId")
			q.Set("IncludeItemTypes", CollectionTypeToItemType(collectionType))
			q.Set("Recursive", "true")
			return q
		})
		return
	}

	sc, err := h.pool.ForUser(c.Request.Context(), serverID, userFromCtx(c))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	query := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET", "/items", query, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetItem handles GET /Items/:itemId.
func (h *MediaHandler) GetItem(c *gin.Context) {
	itemID := c.Param("itemId")

	// Merged virtual library — return a synthetic CollectionFolder item.
	if collectionType, ok := idtrans.DecodeMerged(itemID); ok {
		c.JSON(http.StatusOK, gin.H{
			"Id":             itemID,
			"Name":           mergedDisplayName(collectionType),
			"ServerId":       dashlessID(h.cfg.ServerID),
			"Type":           "CollectionFolder",
			"CollectionType": collectionType,
			"IsFolder":       true,
		})
		return
	}

	sc, backendID, err := h.routeByID(c, itemID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	query := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET", "/items/"+backendID, query, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetUserItems handles GET /Users/:userId/Items.
// Routes to the backend identified by the parentid query param.
// When parentid is a merged virtual ID, fans out to all backends that expose a
// library of that collectiontype and concatenates their results.
func (h *MediaHandler) GetUserItems(c *gin.Context) {
	parentID := queryParam(c, "parentid")
	if parentID == "" {
		// No parentId — if there is a search term, fan out to all backends.
		// Otherwise there is nothing to return.
		if queryParam(c, "searchterm") == "" {
			c.JSON(http.StatusOK, emptyPagedList())
			return
		}
		h.aggregatePagedItems(c, "/items",
			func(sc *backend.ServerClient) url.Values {
				return ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
			},
		)
		return
	}

	if collectionType, ok := idtrans.DecodeMerged(parentID); ok {
		h.aggregatePagedItems(c, "/items",
			func(sc *backend.ServerClient) url.Values {
				q := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
				q.Del("ParentId")
				q.Set("IncludeItemTypes", CollectionTypeToItemType(collectionType))
				q.Set("Recursive", "true")
				return q
			},
		)
		return
	}

	serverID, _, err := idtrans.Decode(parentID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid parentid"})
		return
	}

	sc, err := h.pool.ForUser(c.Request.Context(), serverID, userFromCtx(c))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	query := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET",
		"/users/"+sc.BackendUserID()+"/items", query, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetLatestItems handles GET /Users/:userId/Items/Latest.
// Returns a bare JSON array (Jellyfin's format for this endpoint).
// When parentid is a merged virtual ID, fans out to all backends.
func (h *MediaHandler) GetLatestItems(c *gin.Context) {
	parentID := queryParam(c, "parentid")

	if collectionType, ok := idtrans.DecodeMerged(parentID); ok {
		user := userFromCtx(c)
		clients, err := h.pool.AllForUser(c.Request.Context(), user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		type result struct {
			items []json.RawMessage
		}
		results := make([]result, len(clients))
		var wg sync.WaitGroup
		for i, sc := range clients {
			wg.Add(1)
			go func(i int, sc *backend.ServerClient) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(c.Request.Context(), fanOutTimeout)
				defer cancel()
				q := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
				q.Del("ParentId")
				q.Set("IncludeItemTypes", CollectionTypeToItemType(collectionType))
				body, status, err := sc.ProxyJSON(ctx, "GET",
					"/users/"+sc.BackendUserID()+"/items/Latest", q, nil)
				if err != nil || status != http.StatusOK {
					return
				}
				var items []json.RawMessage
				if err := json.Unmarshal(body, &items); err != nil {
					return
				}
				results[i] = result{items: items}
			}(i, sc)
		}
		wg.Wait()

		var allItems []json.RawMessage
		for _, r := range results {
			allItems = append(allItems, r.items...)
		}
		if allItems == nil {
			allItems = []json.RawMessage{}
		}
		sortByDateCreated(allItems)
		c.JSON(http.StatusOK, allItems)
		return
	}

	if parentID == "" {
		// No parentId — fan out to all backends (Android TV calls
		// GET /Items/Latest with includeItemTypes but no parentId).
		user := userFromCtx(c)
		clients, err := h.pool.AllForUser(c.Request.Context(), user)
		if err != nil || len(clients) == 0 {
			c.JSON(http.StatusOK, []interface{}{})
			return
		}
		type result struct {
			items []json.RawMessage
		}
		results := make([]result, len(clients))
		var wg sync.WaitGroup
		for i, sc := range clients {
			wg.Add(1)
			go func(i int, sc *backend.ServerClient) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(c.Request.Context(), fanOutTimeout)
				defer cancel()
				q := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
				body, status, err := sc.ProxyJSON(ctx, "GET",
					"/users/"+sc.BackendUserID()+"/items/Latest", q, nil)
				if err != nil || status != http.StatusOK {
					return
				}
				var items []json.RawMessage
				if err := json.Unmarshal(body, &items); err != nil {
					return
				}
				results[i] = result{items: items}
			}(i, sc)
		}
		wg.Wait()

		var allItems []json.RawMessage
		for _, r := range results {
			allItems = append(allItems, r.items...)
		}
		if allItems == nil {
			allItems = []json.RawMessage{}
		}
		sortByDateCreated(allItems)
		c.JSON(http.StatusOK, allItems)
		return
	}

	serverID, _, err := idtrans.Decode(parentID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid parentid"})
		return
	}

	sc, err := h.pool.ForUser(c.Request.Context(), serverID, userFromCtx(c))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	query := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET",
		"/users/"+sc.BackendUserID()+"/items/Latest", query, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetUserItem handles GET /Users/:userId/Items/:itemId.
// Jellyfin clients use this user-scoped variant to fetch a single item with
// user-specific data (played state, user rating, etc.).
func (h *MediaHandler) GetUserItem(c *gin.Context) {
	itemID := c.Param("itemId")

	// Merged virtual library — return a synthetic CollectionFolder item.
	if collectionType, ok := idtrans.DecodeMerged(itemID); ok {
		c.JSON(http.StatusOK, gin.H{
			"Id":             itemID,
			"Name":           mergedDisplayName(collectionType),
			"ServerId":       dashlessID(h.cfg.ServerID),
			"Type":           "CollectionFolder",
			"CollectionType": collectionType,
			"IsFolder":       true,
		})
		return
	}

	sc, backendID, err := h.routeByID(c, itemID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	query := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET",
		"/users/"+sc.BackendUserID()+"/items/"+backendID, query, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetResumeItems handles GET /Users/:userId/Items/Resume — aggregates across backends.
func (h *MediaHandler) GetResumeItems(c *gin.Context) {
	h.aggregatePagedItemsFn(c,
		func(sc *backend.ServerClient) string {
			return "/users/" + sc.BackendUserID() + "/items/Resume"
		},
		func(sc *backend.ServerClient) url.Values {
			return ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
		},
	)
}

// GetSuggestedItems handles GET /Items/Suggestions — aggregates across backends.
func (h *MediaHandler) GetSuggestedItems(c *gin.Context) {
	h.aggregatePagedItems(c, "/items/Suggestions", func(sc *backend.ServerClient) url.Values {
		return ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	})
}

// GetItemCounts handles GET /Items/Counts.
// Aggregates item type counts across all backends the user has access to.
func (h *MediaHandler) GetItemCounts(c *gin.Context) {
	user := userFromCtx(c)
	clients, err := h.pool.AllForUser(c.Request.Context(), user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	type countResult struct {
		counts map[string]int
	}
	results := make([]countResult, len(clients))
	var wg sync.WaitGroup
	for i, sc := range clients {
		wg.Add(1)
		go func(i int, sc *backend.ServerClient) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(c.Request.Context(), fanOutTimeout)
			defer cancel()
			q := url.Values{}
			q.Set("UserId", sc.BackendUserID())
			body, status, err := sc.ProxyJSON(ctx, "GET", "/items/Counts", q, nil)
			if err != nil || status != http.StatusOK {
				return
			}
			var counts map[string]int
			if err := json.Unmarshal(body, &counts); err != nil {
				return
			}
			results[i] = countResult{counts: counts}
		}(i, sc)
	}
	wg.Wait()

	totals := map[string]int{
		"MovieCount": 0, "SeriesCount": 0, "EpisodeCount": 0,
		"ArtistCount": 0, "ProgramCount": 0, "TrailerCount": 0,
		"SongCount": 0, "AlbumCount": 0, "MusicVideoCount": 0,
		"BoxSetCount": 0, "BookCount": 0, "ItemCount": 0,
	}
	for _, r := range results {
		for k, v := range r.counts {
			totals[k] += v
		}
	}
	c.JSON(http.StatusOK, totals)
}

// GetItemChildren handles GET /Items/:itemId/children.
// Used by some Jellyfin clients to browse series→seasons→episodes hierarchically.
func (h *MediaHandler) GetItemChildren(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET", "/items/"+backendID+"/children", q, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// UpdateItem handles POST /Items/:itemId (metadata update).
func (h *MediaHandler) UpdateItem(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodySize))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not read body"})
		return
	}
	respBody, status, err := sc.ProxyJSON(c.Request.Context(), "POST", "/items/"+backendID, nil, body)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, respBody, status)
}

// DeleteItem handles DELETE /Items/:itemId.
func (h *MediaHandler) DeleteItem(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	body, status, err := sc.ProxyJSON(c.Request.Context(), "DELETE", "/items/"+backendID, nil, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// RefreshItem handles POST /Items/:itemId/refresh.
func (h *MediaHandler) RefreshItem(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "POST", "/items/"+backendID+"/refresh", q, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetSpecialFeatures handles GET /Items/:itemId/specialfeatures.
func (h *MediaHandler) GetSpecialFeatures(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET", "/items/"+backendID+"/specialfeatures", q, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetThemeMedia handles GET /Items/:itemId/thememedia.
func (h *MediaHandler) GetThemeMedia(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET", "/items/"+backendID+"/thememedia", q, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetLocalTrailers handles GET /Users/:userId/Items/:itemId/localtrailers.
func (h *MediaHandler) GetLocalTrailers(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET",
		"/users/"+sc.BackendUserID()+"/items/"+backendID+"/localtrailers", q, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetIntros handles GET /Users/:userId/Items/:itemId/intros.
func (h *MediaHandler) GetIntros(c *gin.Context) {
	sc, backendID, err := h.routeByID(c, c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET",
		"/users/"+sc.BackendUserID()+"/items/"+backendID+"/intros", nil, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// GetQueryFilters handles GET /Items/Filters2 and GET /Items/Filters.
// Routes to the backend identified by ParentId; returns empty filters if absent.
func (h *MediaHandler) GetQueryFilters(c *gin.Context) {
	serverID := ServerIDFromQuery(c)
	if serverID == "" {
		c.JSON(http.StatusOK, gin.H{
			"Genres":          []interface{}{},
			"Tags":            []interface{}{},
			"OfficialRatings": []interface{}{},
			"Years":           []interface{}{},
		})
		return
	}

	// Merged virtual library — fan out to all backends and merge filters.
	if collectionType, ok := idtrans.DecodeMerged(serverID); ok {
		h.aggregateFilters(c, collectionType)
		return
	}

	sc, err := h.pool.ForUser(c.Request.Context(), serverID, userFromCtx(c))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	q := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
	body, status, err := sc.ProxyJSON(c.Request.Context(), "GET", "/items/Filters2", q, nil)
	if err != nil {
		gatewayError(c, err)
		return
	}
	writeJSON(c, body, status)
}

// aggregateFilters fans out a Filters2 request to all backends the user is
// mapped to and merges the results, deduplicating by name/value.
func (h *MediaHandler) aggregateFilters(c *gin.Context, collectionType string) {
	user := userFromCtx(c)
	clients, err := h.pool.AllForUser(c.Request.Context(), user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	type nameItem struct {
		Name string `json:"Name"`
	}
	type filterResult struct {
		Genres          []nameItem `json:"Genres"`
		Tags            []nameItem `json:"Tags"`
		OfficialRatings []string   `json:"OfficialRatings"`
		Years           []int      `json:"Years"`
	}
	results := make([]filterResult, len(clients))
	var wg sync.WaitGroup
	for i, sc := range clients {
		wg.Add(1)
		go func(i int, sc *backend.ServerClient) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(c.Request.Context(), fanOutTimeout)
			defer cancel()
			q := ForwardQuery(c.Request.URL.Query(), sc.BackendUserID())
			q.Del("ParentId")
			if it := CollectionTypeToItemType(collectionType); it != "" {
				q.Set("IncludeItemTypes", it)
			}
			body, status, err := sc.ProxyJSON(ctx, "GET", "/items/Filters2", q, nil)
			if err != nil || status != http.StatusOK {
				return
			}
			var resp filterResult
			if err := json.Unmarshal(body, &resp); err != nil {
				return
			}
			results[i] = resp
		}(i, sc)
	}
	wg.Wait()

	genreSeen := map[string]bool{}
	tagSeen := map[string]bool{}
	ratingSeen := map[string]bool{}
	yearSeen := map[int]bool{}

	var genres, tags []nameItem
	var ratings []string
	var years []int

	for _, resp := range results {
		for _, g := range resp.Genres {
			if !genreSeen[g.Name] {
				genreSeen[g.Name] = true
				genres = append(genres, g)
			}
		}
		for _, t := range resp.Tags {
			if !tagSeen[t.Name] {
				tagSeen[t.Name] = true
				tags = append(tags, t)
			}
		}
		for _, r := range resp.OfficialRatings {
			if !ratingSeen[r] {
				ratingSeen[r] = true
				ratings = append(ratings, r)
			}
		}
		for _, y := range resp.Years {
			if !yearSeen[y] {
				yearSeen[y] = true
				years = append(years, y)
			}
		}
	}

	if genres == nil {
		genres = []nameItem{}
	}
	if tags == nil {
		tags = []nameItem{}
	}
	if ratings == nil {
		ratings = []string{}
	}
	if years == nil {
		years = []int{}
	}

	c.JSON(http.StatusOK, gin.H{
		"Genres":          genres,
		"Tags":            tags,
		"OfficialRatings": ratings,
		"Years":           years,
	})
}

// GetMediaSegments handles GET /MediaSegments/:itemId.
// Returns intro/outro chapter markers. Not supported cross-backend — return empty.
func (h *MediaHandler) GetMediaSegments(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"Items":            []interface{}{},
		"TotalRecordCount": 0,
	})
}

// sortByDateCreated sorts a slice of JSON items by the DateCreated field
// in descending order (newest first). Items without a parseable date are
// pushed to the end.
func sortByDateCreated(items []json.RawMessage) {
	type dateHolder struct {
		DateCreated string `json:"DateCreated"`
	}
	sort.SliceStable(items, func(i, j int) bool {
		var a, b dateHolder
		_ = json.Unmarshal(items[i], &a)
		_ = json.Unmarshal(items[j], &b)
		// Both empty → stable; one empty → non-empty wins.
		if a.DateCreated == "" {
			return false
		}
		if b.DateCreated == "" {
			return true
		}
		return a.DateCreated > b.DateCreated
	})
}
