package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ddevcap/jellymux/backend"
	"github.com/ddevcap/jellymux/ent"
	entuser "github.com/ddevcap/jellymux/ent/user"
	"github.com/ddevcap/jellymux/idtrans"
	"github.com/gin-gonic/gin"
	"github.com/jellydator/ttlcache/v3"
)

// GetUser handles GET /Users/:userId.
// Always returns the authenticated caller's own profile; the path userId is
// validated but not used to look up a different user.
func (h *MediaHandler) GetUser(c *gin.Context) {
	user := userFromCtx(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	c.JSON(http.StatusOK, BuildUserObject(user, h.cfg))
}

// GetViews handles GET /Users/:userId/views.
// Calls each backend the user is mapped to and merges their library roots.
// Libraries with the same CollectionType across multiple backends are collapsed
// into a single virtual merged view so clients see one "Movies" folder, not one
// per backend.
func (h *MediaHandler) GetViews(c *gin.Context) {
	user := userFromCtx(c)
	if user == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no user"})
		return
	}

	// Check cache first.
	if item := h.viewCache.Get(user.ID.String()); item != nil {
		writePagedViews(c, item.Value())
		return
	}

	allItems, err := h.mergedViews(c.Request.Context(), user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.viewCache.Set(user.ID.String(), allItems, ttlcache.DefaultTTL)
	writePagedViews(c, allItems)
}

// GetUsers handles GET /Users.
// Returns all proxy users in Jellyfin user-object format.
// Used by the Jellyfin web UI admin panel to list users.
func (h *MediaHandler) GetUsers(c *gin.Context) {
	users, err := h.db.User.Query().Order(entuser.ByUsername()).All(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list users"})
		return
	}
	resp := make([]gin.H, len(users))
	for i, u := range users {
		resp[i] = BuildUserObject(u, h.cfg)
	}
	c.JSON(http.StatusOK, resp)
}

// GetUserViews handles GET /UserViews.
// Alias for /Users/:userId/views used by some Jellyfin clients and the web UI.
func (h *MediaHandler) GetUserViews(c *gin.Context) {
	user := userFromCtx(c)
	if user == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no user"})
		return
	}

	if item := h.viewCache.Get(user.ID.String()); item != nil {
		writePagedViews(c, item.Value())
		return
	}

	allItems, err := h.mergedViews(c.Request.Context(), user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.viewCache.Set(user.ID.String(), allItems, ttlcache.DefaultTTL)
	writePagedViews(c, allItems)
}

// mergedViews fetches library views from all backends the user is mapped to,
// groups them by CollectionType, and collapses same-type libraries from
// multiple backends into a single virtual merged view. Libraries with a type
// that appears on only one backend are passed through unchanged, as are
// libraries with an empty/unknown CollectionType.
//
// The merged virtual view has its Id replaced with idtrans.EncodeMerged(type),
// e.g. "merged_movies", so that item-browsing requests can be fanned out to
// all contributing backends.
func (h *MediaHandler) mergedViews(ctx context.Context, user *ent.User) ([]json.RawMessage, error) {
	clients, err := h.pool.AllForUser(ctx, user)
	if err != nil {
		return nil, err
	}

	type viewEntry struct {
		raw            json.RawMessage
		collectionType string
		count          int
	}

	var order []string
	byType := make(map[string]*viewEntry)

	type backendResult struct {
		items []json.RawMessage
	}
	backendResults := make([]backendResult, len(clients))
	var wg sync.WaitGroup
	for i, sc := range clients {
		wg.Add(1)
		go func(i int, sc *backend.ServerClient) {
			defer wg.Done()
			fanCtx, cancel := context.WithTimeout(ctx, fanOutTimeout)
			defer cancel()
			body, status, err := sc.ProxyJSON(fanCtx, "GET",
				"/users/"+sc.BackendUserID()+"/views", nil, nil)
			if err != nil || status != http.StatusOK {
				return
			}
			var resp struct {
				Items []json.RawMessage `json:"Items"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return
			}
			backendResults[i] = backendResult{items: resp.Items}
		}(i, sc)
	}
	wg.Wait()

	for _, br := range backendResults {
		for _, raw := range br.items {
			var meta struct {
				Id             string `json:"Id"`
				CollectionType string `json:"CollectionType"`
			}
			if err := json.Unmarshal(raw, &meta); err != nil {
				continue
			}
			ct := strings.ToLower(meta.CollectionType)
			if ct == "" {
				// Unknown type — pass through as a unique entry keyed by its ID.
				key := "unknown_" + meta.Id
				order = append(order, key)
				byType[key] = &viewEntry{raw: raw, collectionType: ct, count: 1}
				continue
			}
			if existing, ok := byType[ct]; ok {
				existing.count++
			} else {
				order = append(order, ct)
				byType[ct] = &viewEntry{raw: raw, collectionType: ct, count: 1}
			}
		}
	}

	var result []json.RawMessage
	for _, key := range order {
		entry := byType[key]
		if entry.count <= 1 || entry.collectionType == "" {
			result = append(result, entry.raw)
			continue
		}
		// Multiple backends share this CollectionType — replace its Id with the
		// virtual merged ID so clients use it as ParentId when browsing in.
		var base map[string]interface{}
		if err := json.Unmarshal(entry.raw, &base); err != nil {
			result = append(result, entry.raw)
			continue
		}
		base["Id"] = idtrans.EncodeMerged(entry.collectionType)
		merged, err := json.Marshal(base)
		if err != nil {
			result = append(result, entry.raw)
			continue
		}
		result = append(result, json.RawMessage(merged))
	}

	if result == nil {
		result = []json.RawMessage{}
	}
	return result, nil
}

// mergedDisplayName returns a human-readable name for a merged library type.
func mergedDisplayName(collectionType string) string {
	if m, ok := collectionTypes[collectionType]; ok {
		return m.displayName
	}
	if len(collectionType) == 0 {
		return collectionType
	}
	return strings.ToUpper(collectionType[:1]) + collectionType[1:]
}

// aggregatePagedItems fans a GET request out to every backend the user has
// access to, merges the Items arrays and returns a combined paged result.
// path must be a backend path like "/items" or "/artists".
// queryFn builds the per-backend query from a ServerClient (allows substituting UserId etc.).
//
// Client-side pagination (StartIndex/Limit) is applied after merging: each
// backend receives the request without pagination constraints so the proxy
// can collect the full set, then the merged result is sliced to the requested
// page before being returned.
func (h *MediaHandler) aggregatePagedItems(
	c *gin.Context,
	path string,
	queryFn func(sc *backend.ServerClient) url.Values,
) {
	h.aggregatePagedItemsFn(c, func(_ *backend.ServerClient) string { return path }, queryFn)
}

// aggregatePagedItemsFn is like aggregatePagedItems but accepts a pathFn that
// can return a different backend path per client (e.g. to embed the backend
// user ID in user-scoped Jellyfin endpoints).
func (h *MediaHandler) aggregatePagedItemsFn(
	c *gin.Context,
	pathFn func(sc *backend.ServerClient) string,
	queryFn func(sc *backend.ServerClient) url.Values,
) {
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
			q := queryFn(sc)
			// Remove per-backend pagination — we paginate the merged result below.
			q.Del("StartIndex")
			q.Del("Limit")
			body, status, err := sc.ProxyJSON(ctx, "GET", pathFn(sc), q, nil)
			if err != nil || status != http.StatusOK {
				return
			}
			var resp struct {
				Items []json.RawMessage `json:"Items"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return
			}
			results[i] = result{items: resp.Items}
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

	// Sort the merged set so paginated results are deterministic.
	sortBy := queryParam(c, "sortby")
	sortOrder := queryParam(c, "sortorder")
	sortMergedItems(allItems, sortBy, sortOrder)

	totalCount := len(allItems)

	// Apply client-requested pagination to the merged set.
	startIndex := 0
	if s := queryParam(c, "startindex"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			startIndex = v
		}
	}
	if startIndex > len(allItems) {
		startIndex = len(allItems)
	}
	allItems = allItems[startIndex:]

	if s := queryParam(c, "limit"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 && v < len(allItems) {
			allItems = allItems[:v]
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"Items":            allItems,
		"TotalRecordCount": totalCount,
		"StartIndex":       startIndex,
	})
}

// writePagedViews writes a standard paged views response.
func writePagedViews(c *gin.Context, items []json.RawMessage) {
	c.JSON(http.StatusOK, gin.H{
		"Items":            items,
		"TotalRecordCount": len(items),
		"StartIndex":       0,
	})
}

// sortMergedItems sorts a slice of JSON items by the given Jellyfin SortBy
// field. Supports the most common sort fields: SortName, DateCreated,
// PremiereDate, CommunityRating, ProductionYear. Falls back to SortName.
// SortOrder "Descending" reverses the result.
func sortMergedItems(items []json.RawMessage, sortBy, sortOrder string) {
	if len(items) < 2 {
		return
	}

	field := "SortName"
	if sortBy != "" {
		field = strings.SplitN(sortBy, ",", 2)[0] // take first field only
	}
	desc := strings.EqualFold(sortOrder, "descending")

	type sortable struct {
		SortName        string  `json:"SortName"`
		Name            string  `json:"Name"`
		DateCreated     string  `json:"DateCreated"`
		PremiereDate    string  `json:"PremiereDate"`
		CommunityRating float64 `json:"CommunityRating"`
		ProductionYear  int     `json:"ProductionYear"`
	}
	parsed := make([]sortable, len(items))
	for i, raw := range items {
		_ = json.Unmarshal(raw, &parsed[i])
	}

	sort.SliceStable(items, func(i, j int) bool {
		a, b := parsed[i], parsed[j]
		var less bool
		switch strings.ToLower(field) {
		case "datecreated":
			less = a.DateCreated < b.DateCreated
		case "premieredate":
			less = a.PremiereDate < b.PremiereDate
		case "communityrating":
			less = a.CommunityRating < b.CommunityRating
		case "productionyear":
			less = a.ProductionYear < b.ProductionYear
		default: // SortName
			nameA := a.SortName
			if nameA == "" {
				nameA = a.Name
			}
			nameB := b.SortName
			if nameB == "" {
				nameB = b.Name
			}
			less = strings.ToLower(nameA) < strings.ToLower(nameB)
		}
		if desc {
			return !less
		}
		return less
	})
}
