package idtrans

import "encoding/json"

// BackendInfo carries metadata about the backend server that sourced a
// response. When non-nil, its fields are injected into every JSON object
// that contains an "Id" key (i.e. Jellyfin item objects).
type BackendInfo struct {
	ID   string
	Name string
	URL  string
}

// idFields is the set of JSON object keys whose string values are single item
// IDs that must be encoded or decoded when crossing the proxy boundary.
// Keep this list in sync with the Jellyfin API surface as new endpoints are added.
var idFields = map[string]bool{
	"Id":                       true,
	"ParentId":                 true,
	"SeriesId":                 true,
	"SeasonId":                 true,
	"AlbumId":                  true,
	"ItemId":                   true, // present in UserData objects
	"ChannelId":                true,
	"PlaylistItemId":           true,
	"ParentBackdropItemId":     true,
	"ParentThumbItemId":        true,
	"ParentLogoItemId":         true,
	"ParentArtItemId":          true,
	"ParentPrimaryImageItemId": true,
	"EpisodeId":                true,
	"MovieId":                  true,
	"MediaSourceId":            true, // appears in PlaybackInfo request bodies
}

// serverIDFields are keys whose string values identify a Jellyfin server.
// In responses these are replaced with the proxy's own server ID so that
// clients never learn the addresses of the backend servers.
var serverIDFields = map[string]bool{
	"ServerId": true,
}

// RewriteResponse encodes all item IDs in a backend JSON response using the
// backend's externalID as namespace, and replaces all server ID fields with
// proxyServerID.
//
// The returned bytes are a freshly marshalled JSON document.
func RewriteResponse(b []byte, externalID, proxyServerID string, backend *BackendInfo) ([]byte, error) {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	rewriteNode(v, func(id string) string { return Encode(externalID, id) }, proxyServerID, backend)
	return json.Marshal(v)
}

// RewriteRequest decodes proxy UUIDs back to backend item IDs in all item ID
// fields of a JSON request body before it is forwarded to a backend server.
// Server ID fields are left untouched.
func RewriteRequest(b []byte) ([]byte, error) {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	rewriteNode(v, func(id string) string {
		_, itemID, err := Decode(id)
		if err != nil {
			return id // not a proxy ID — pass through unchanged
		}
		return itemID
	}, "" /* do not touch server IDs in outgoing requests */, nil)
	return json.Marshal(v)
}

// rewriteNode recursively walks v and applies transformID to every value
// whose key is in idFields, and replaces every value whose key is in
// serverIDFields with proxyServerID (when non-empty).
//
// When backend is non-nil and the current map contains an "Id" key,
// BackendId, BackendName, and BackendUrl are injected into the object.
//
// It modifies the tree in place; v must be the result of json.Unmarshal
// into an interface{} so the maps are writable.
func rewriteNode(v interface{}, transformID func(string) string, proxyServerID string, backend *BackendInfo) {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, child := range val {
			switch {
			case idFields[k]:
				if s, ok := child.(string); ok && s != "" {
					val[k] = transformID(s)
				}
			case serverIDFields[k] && proxyServerID != "":
				val[k] = proxyServerID
			default:
				// Not a recognized ID field — recurse in case it's a
				// nested object or array that contains ID fields.
				rewriteNode(child, transformID, proxyServerID, backend)
			}
		}
		if backend != nil {
			if _, hasID := val["Id"]; hasID {
				val["BackendId"] = backend.ID
				val["BackendName"] = backend.Name
				val["BackendUrl"] = backend.URL
			}
		}
	case []interface{}:
		for _, elem := range val {
			rewriteNode(elem, transformID, proxyServerID, backend)
		}
	}
}
