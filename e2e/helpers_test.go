//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ── HTTP helpers ─────────────────────────────────────────────────────────────

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
	// Do NOT follow redirects — we want to inspect 302s.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// get performs a GET request with an optional auth token.
func get(url, token string) *http.Response {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		panic(fmt.Sprintf("e2e: failed to create GET request: %v", err))
	}
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(fmt.Sprintf("e2e: GET %s failed: %v", url, err))
	}
	return resp
}

// post performs a POST request with a JSON body and optional auth token.
func post(url string, body interface{}, token string) *http.Response {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			panic(fmt.Sprintf("e2e: failed to marshal body: %v", err))
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, url, bodyReader)
	if err != nil {
		panic(fmt.Sprintf("e2e: failed to create POST request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(fmt.Sprintf("e2e: POST %s failed: %v", url, err))
	}
	return resp
}

// del performs a DELETE request with an optional auth token.
func del(url, token string) *http.Response {
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		panic(fmt.Sprintf("e2e: failed to create DELETE request: %v", err))
	}
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(fmt.Sprintf("e2e: DELETE %s failed: %v", url, err))
	}
	return resp
}

// ── JSON helpers ─────────────────────────────────────────────────────────────

// parseJSONObject reads and parses a JSON response body into a map.
func parseJSONObject(resp *http.Response) map[string]interface{} {
	defer resp.Body.Close()
	var result map[string]interface{}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(fmt.Sprintf("e2e: failed to read response body: %v", err))
	}
	if err := json.Unmarshal(body, &result); err != nil {
		panic(fmt.Sprintf("e2e: failed to parse JSON object: %v\nbody: %s", err, string(body)))
	}
	return result
}

// parseJSONArray reads and parses a JSON response body into a slice.
func parseJSONArray(resp *http.Response) []map[string]interface{} {
	defer resp.Body.Close()
	var result []map[string]interface{}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(fmt.Sprintf("e2e: failed to read response body: %v", err))
	}
	if err := json.Unmarshal(body, &result); err != nil {
		panic(fmt.Sprintf("e2e: failed to parse JSON array: %v\nbody: %s", err, string(body)))
	}
	return result
}

// pagedItems extracts the Items array from a paged response.
func pagedItems(resp *http.Response) ([]interface{}, int) {
	body := parseJSONObject(resp)
	items, _ := body["Items"].([]interface{})
	total := int(body["TotalRecordCount"].(float64))
	return items, total
}

// ── URL helpers ──────────────────────────────────────────────────────────────

// proxyURL builds a full URL to the proxy.
func proxyURL(pathAndQuery string) string {
	if !strings.HasPrefix(pathAndQuery, "/") {
		pathAndQuery = "/" + pathAndQuery
	}
	return proxyBase + pathAndQuery
}

