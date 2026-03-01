// Package idtrans handles translation between backend Jellyfin item IDs and
// the proxy-scoped IDs exposed to clients.
//
// All proxy IDs are 32-character lowercase hex strings (dashless UUIDs) so
// that the Jellyfin Kotlin SDK (and other strict clients) can parse them.
//
// Encoding uses UUID v5 (SHA-1) with a per-server namespace derived from the
// backend's external_id. The mapping is cached in memory for O(1)
// reverse lookups. No database table is required.
package idtrans

import (
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// ── Encode / Decode ──────────────────────────────────────────────────────────

// baseNamespace is a fixed UUID used as the root namespace for all proxy IDs.
// Per-server namespaces are derived from this via UUID v5.
var baseNamespace = uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")

// serverNamespaces caches the per-server UUID v5 namespace so we don't
// recompute it on every Encode call.
var serverNamespaces sync.Map // map[string]uuid.UUID

func serverNamespace(serverID string) uuid.UUID {
	if v, ok := serverNamespaces.Load(serverID); ok {
		return v.(uuid.UUID)
	}
	ns := uuid.NewSHA1(baseNamespace, []byte(serverID))
	serverNamespaces.Store(serverID, ns)
	return ns
}

// idMapping holds the original (serverID, itemID) pair for a proxy UUID.
type idMapping struct {
	serverID string
	itemID   string
}

var (
	// forward: "serverID\x00itemID" → dashless proxy UUID
	forwardCache sync.Map // map[string]string
	// reverse: dashless proxy UUID → idMapping
	//
	// TODO: consider LRU eviction for long-running instances — every unique
	// (serverID, itemID) pair ever seen stays in memory indefinitely.
	reverseCache sync.Map // map[string]idMapping
)

func forwardKey(serverID, itemID string) string {
	return serverID + "\x00" + itemID
}

// Encode creates a proxy-scoped 32-char hex ID (dashless UUID v5) from a
// backend's external_id and the item's original ID on that backend. The
// mapping is cached for O(1) reverse lookup via Decode.
// Returns an empty string if itemID is empty.
func Encode(serverID, itemID string) string {
	if itemID == "" {
		return ""
	}

	key := forwardKey(serverID, itemID)

	// Fast path: check cache.
	if v, ok := forwardCache.Load(key); ok {
		return v.(string)
	}

	// Generate deterministic UUID v5.
	ns := serverNamespace(serverID)
	proxyUUID := uuid.NewSHA1(ns, []byte(itemID))
	dashless := strings.ReplaceAll(proxyUUID.String(), "-", "")

	// Store in both directions.
	forwardCache.Store(key, dashless)
	reverseCache.Store(dashless, idMapping{serverID: serverID, itemID: itemID})

	return dashless
}

// Decode extracts the backend server ID and original item ID from a proxy ID.
//
// It checks the in-memory reverse cache first, then falls back to the legacy
// "prefix_itemID" format for backward compatibility during migration.
func Decode(proxyID string) (serverID, itemID string, err error) {
	// Legacy format: "prefix_itemID"
	if strings.Contains(proxyID, "_") {
		idx := strings.Index(proxyID, "_")
		if idx <= 0 {
			return "", proxyID, fmt.Errorf("idtrans: %q has no server prefix", proxyID)
		}
		return proxyID[:idx], proxyID[idx+1:], nil
	}

	// Strip dashes if the client sent a dashed UUID.
	normalised := strings.ReplaceAll(proxyID, "-", "")

	// Look up in reverse cache.
	if v, ok := reverseCache.Load(normalised); ok {
		m := v.(idMapping)
		return m.serverID, m.itemID, nil
	}

	return "", proxyID, fmt.Errorf("idtrans: %q not found in ID cache", proxyID)
}

// DecodeServerID returns only the server ID from a proxy ID.
func DecodeServerID(proxyID string) (string, error) {
	serverID, _, err := Decode(proxyID)
	return serverID, err
}

// ── Merged virtual library IDs ───────────────────────────────────────────────
//
// Merged IDs represent virtual libraries that combine the same collection type
// across multiple backends (e.g. one "Movies" view for two servers).
//
// The Jellyfin SDK expects ALL item IDs to be valid UUIDs. We use deterministic
// UUID v5 values derived from a fixed namespace + the collection type string.

// mergedNamespace is a fixed UUID used as the namespace for merged library IDs.
var mergedNamespace = uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890")

// mergedToUUID caches the collectionType → dashless-UUID mapping.
var mergedToUUID sync.Map // map[string]string

// uuidToMerged caches the dashless-UUID → collectionType reverse mapping.
var uuidToMerged sync.Map // map[string]string

func mergedUUID(collectionType string) string {
	if v, ok := mergedToUUID.Load(collectionType); ok {
		return v.(string)
	}
	id := uuid.NewSHA1(mergedNamespace, []byte(collectionType))
	dashless := strings.ReplaceAll(id.String(), "-", "")
	mergedToUUID.Store(collectionType, dashless)
	uuidToMerged.Store(dashless, collectionType)
	return dashless
}

// EncodeMerged returns a deterministic dashless UUID for a merged library view
// keyed by Jellyfin CollectionType (e.g. "movies", "tvshows").
func EncodeMerged(collectionType string) string {
	return mergedUUID(collectionType)
}

// DecodeMerged returns the CollectionType if proxyID is a merged virtual ID.
func DecodeMerged(proxyID string) (collectionType string, ok bool) {
	// Normalise: strip dashes if the client sent a dashed UUID.
	normalised := strings.ReplaceAll(proxyID, "-", "")

	// Check reverse cache first (UUID → collectionType).
	if v, loaded := uuidToMerged.Load(normalised); loaded {
		return v.(string), true
	}

	// Backward compatibility: accept the old "merged_<type>" format.
	const legacyPrefix = "merged_"
	if strings.HasPrefix(proxyID, legacyPrefix) {
		ct := proxyID[len(legacyPrefix):]
		if ct != "" {
			return ct, true
		}
	}

	return "", false
}

// PrewarmMerged populates the merged ID caches for all known Jellyfin
// collection types so that DecodeMerged works from the first request.
func PrewarmMerged() {
	for _, ct := range []string{
		"movies", "tvshows", "music", "books", "boxsets",
		"musicvideos", "photos", "homevideos", "livetv",
		"playlists", "folders", "trailers", "podcasts",
	} {
		mergedUUID(ct)
	}
}
