package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/ddevcap/jellyfin-proxy/backend"
)

// syncWatchStateTimeout is the per-backend deadline for cross-backend
// watch-state sync operations. These run in the background and should
// not block the client response.
const syncWatchStateTimeout = 10 * time.Second

// syncWatchState propagates a played/favorite action to all other backends
// the user has access to. It fetches the item's ProviderIds (TMDB, IMDB)
// from the source backend, searches each other backend for a matching item,
// and applies the same action.
//
// This runs as a fire-and-forget goroutine — failures are logged but don't
// affect the client response.
func (h *MediaHandler) syncWatchState(
	sourceServerID string,
	sourceBackendID string,
	sourceSC *backend.ServerClient,
	method string,
	collection string,
	allClients []*backend.ServerClient,
) {
	ctx, cancel := context.WithTimeout(context.Background(), syncWatchStateTimeout)
	defer cancel()

	// 1. Fetch the item from the source backend to get ProviderIds.
	body, status, err := sourceSC.ProxyJSON(ctx, "GET",
		"/users/"+sourceSC.BackendUserID()+"/items/"+sourceBackendID, nil, nil)
	if err != nil || status != http.StatusOK {
		return
	}

	var item struct {
		ProviderIds map[string]string `json:"ProviderIds"`
		Type        string            `json:"Type"`
	}
	if err := json.Unmarshal(body, &item); err != nil || len(item.ProviderIds) == 0 {
		return // no provider IDs — can't match across backends
	}

	// Pick the best ID for matching: prefer TMDB, fall back to IMDB.
	matchParam := ""
	matchValue := ""
	if v, ok := item.ProviderIds["Tmdb"]; ok && v != "" {
		matchParam = "HasTmdbId"
		matchValue = v
	} else if v, ok := item.ProviderIds["Imdb"]; ok && v != "" {
		matchParam = "HasImdbId"
		matchValue = v
	}
	if matchParam == "" {
		return
	}

	// 2. For each other backend, search for a matching item and apply the action.
	for _, sc := range allClients {
		if sc.ExternalID() == sourceServerID {
			continue // skip the source backend
		}

		go func(sc *backend.ServerClient) {
			syncCtx, syncCancel := context.WithTimeout(context.Background(), syncWatchStateTimeout)
			defer syncCancel()

			// Search for the item by provider ID.
			q := url.Values{}
			q.Set("Recursive", "true")
			q.Set("IncludeItemTypes", item.Type)
			if matchParam == "HasTmdbId" {
				q.Set("HasTmdbId", "true")
			} else {
				q.Set("HasImdbId", "true")
			}
			q.Set("Fields", "ProviderIds")
			q.Set("Limit", "50")

			searchBody, searchStatus, err := sc.ProxyJSON(syncCtx, "GET",
				"/users/"+sc.BackendUserID()+"/items", q, nil)
			if err != nil || searchStatus != http.StatusOK {
				return
			}

			var resp struct {
				Items []json.RawMessage `json:"Items"`
			}
			if err := json.Unmarshal(searchBody, &resp); err != nil {
				return
			}

			// Find the matching item by provider ID.
			for _, rawItem := range resp.Items {
				var candidate struct {
					Id          string            `json:"Id"`
					ProviderIds map[string]string `json:"ProviderIds"`
				}
				if err := json.Unmarshal(rawItem, &candidate); err != nil {
					continue
				}

				matched := false
				if matchParam == "HasTmdbId" {
					matched = candidate.ProviderIds["Tmdb"] == matchValue
				} else {
					matched = candidate.ProviderIds["Imdb"] == matchValue
				}

				if !matched {
					continue
				}

				// Apply the same action to the matched item.
				var path string
				if collection == "Items" {
					path = "/users/" + sc.BackendUserID() + "/items/" + candidate.Id + "/rating"
				} else {
					path = "/users/" + sc.BackendUserID() + "/" + collection + "/" + candidate.Id
				}

				_, actionStatus, err := sc.ProxyJSON(syncCtx, method, path, nil, nil)
				if err != nil {
					slog.Debug("watch-state sync failed",
						"backend", sc.ExternalID(),
						"item", candidate.Id,
						"error", err)
				} else {
					slog.Info("watch-state synced",
						"backend", sc.ExternalID(),
						"item", candidate.Id,
						"action", method+" "+collection,
						"status", actionStatus)
				}
				break // only sync one match per backend
			}
		}(sc)
	}
}
