package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ddevcap/jellymux/ent"
	"github.com/ddevcap/jellymux/idtrans"
)

// ServerClient is a ready-to-use HTTP client for one backend Jellyfin server
// with user credentials already resolved. Obtain one via Pool.ForUser or
// Pool.ForBackend — do not construct directly.
type ServerClient struct {
	backend       *ent.Backend
	token         string
	backendUserID string // the user's ID on this specific backend server
	pool          *Pool
}

func (sc *ServerClient) ExternalID() string    { return sc.backend.ExternalID }
func (sc *ServerClient) BackendUserID() string { return sc.backendUserID }
func (sc *ServerClient) ServerURL() string     { return strings.TrimRight(sc.backend.URL, "/") }
func (sc *ServerClient) Token() string         { return sc.token }

// DirectURL builds a fully-qualified backend URL with ApiKey injected.
// Used for direct-stream redirects (302) so the client fetches from the
// backend without going through the proxy.
func (sc *ServerClient) DirectURL(path string, query url.Values) string {
	q := make(url.Values, len(query)+1)
	for k, v := range query {
		q[k] = v
	}
	if sc.token != "" {
		q.Set("ApiKey", sc.token)
	}
	return strings.TrimRight(sc.backend.URL, "/") + path + "?" + q.Encode()
}

// ProxyJSON forwards a request to the backend, buffers the full response,
// rewrites all item IDs and server references, and returns the translated body
// with the backend's HTTP status code.
//
// Non-2xx responses are returned as-is without ID rewriting — they contain
// error messages, not item data.
//
// A network-level failure is returned as a non-nil error; HTTP-level failures
// (4xx, 5xx) are signalled only via the returned status code.
func (sc *ServerClient) ProxyJSON(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, int, error) {
	req, err := sc.newRequest(ctx, method, path, query, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := sc.pool.jsonClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("backend request to %s failed: %w", sc.backend.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading backend response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || len(raw) == 0 {
		return raw, resp.StatusCode, nil
	}

	bi := &idtrans.BackendInfo{
		ID:   sc.backend.ID.String(),
		Name: sc.backend.Name,
		URL:  sc.backend.URL,
	}
	translated, err := idtrans.RewriteResponse(raw, sc.backend.ExternalID, sc.pool.proxyServerID, bi)
	if err != nil {
		// Non-JSON body (e.g. an image accidentally routed here): pass through.
		return raw, resp.StatusCode, nil
	}
	return translated, resp.StatusCode, nil
}

// ProxyRaw forwards a request to the backend and returns the raw response body
// without any ID rewriting. Used for HLS playlists and other text content that
// needs URL rewriting but not JSON field rewriting.
func (sc *ServerClient) ProxyRaw(ctx context.Context, method, path string, query url.Values) ([]byte, int, error) {
	req, err := sc.newRequest(ctx, method, path, query, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := sc.pool.streamClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("backend request to %s failed: %w", sc.backend.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// ProxyStream forwards a streaming request (video/audio) to the backend and
// pipes the response body directly to w without buffering or ID rewriting.
//
// The Range header is forwarded so clients can seek into the stream.
// Flushes after every write so transcoding segments reach the client
// immediately rather than buffering inside the proxy.
func (sc *ServerClient) ProxyStream(ctx context.Context, method, path string, query url.Values, inHeader http.Header, w http.ResponseWriter) error {
	req, err := sc.newRequest(ctx, method, path, query, nil)
	if err != nil {
		return err
	}

	if r := inHeader.Get("Range"); r != "" {
		req.Header.Set("Range", r)
	}
	// Ask the backend not to buffer either.
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := sc.pool.streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("backend stream request to %s failed: %w", sc.backend.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	copyStreamHeaders(resp.Header, w.Header())
	// Force chunked transfer so the client receives bytes as they arrive
	// rather than waiting for Content-Length to be known.
	if resp.Header.Get("Content-Length") == "" {
		w.Header().Set("Transfer-Encoding", "chunked")
	}
	w.WriteHeader(resp.StatusCode)

	// Flush-on-write: get the flusher once, then copy in chunks and flush.
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// newRequest builds an authenticated HTTP request for the backend server.
// If body is non-nil, proxy UUIDs are decoded back to backend IDs and any
// UserId is replaced with the backend user ID.
func (sc *ServerClient) newRequest(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Request, error) {
	var reqBody io.Reader
	if len(body) > 0 {
		translated, err := idtrans.RewriteRequest(body)
		if err != nil {
			translated = body // best-effort: send original on parse failure
		}
		if sc.backendUserID != "" {
			translated = rewriteBodyUserID(translated, sc.backendUserID)
		}
		reqBody = bytes.NewReader(translated)
	}

	u := sc.buildURL(path, query)
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, fmt.Errorf("building backend request: %w", err)
	}
	if sc.token != "" {
		req.Header.Set("X-Emby-Token", sc.token)
	}
	return req, nil
}

// rewriteBodyUserID replaces "UserId" (any casing) in a JSON object body.
func rewriteBodyUserID(body []byte, backendUserID string) []byte {
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return body // not a JSON object — pass through
	}
	changed := false
	for k := range m {
		if strings.EqualFold(k, "userid") {
			m[k] = backendUserID
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

func (sc *ServerClient) buildURL(path string, query url.Values) string {
	u := strings.TrimRight(sc.backend.URL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

// copyStreamHeaders copies only the backend response headers needed for
// media playback.
func copyStreamHeaders(src, dst http.Header) {
	for _, h := range []string{
		"Content-Type",
		"Content-Length",
		"Content-Range",
		"Content-Disposition",
		"Accept-Ranges",
		"X-Content-Duration",
		"Cache-Control",
	} {
		if v := src.Get(h); v != "" {
			dst.Set(h, v)
		}
	}
}
