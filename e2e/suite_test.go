//go:build e2e

// Package e2e contains end-to-end tests that run against a live Docker
// stack (proxy + Postgres + 2 Jellyfin backends).
//
// Run with: go test -tags e2e -v -count=1 -timeout 5m ./e2e/...
// Or:       make e2e
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellyfin-proxy/idtrans"
)

// ── Configurable addresses ───────────────────────────────────────────────────

var (
	// proxyBase is the base URL of the running proxy.
	proxyBase = envOr("E2E_PROXY_URL", "http://localhost:18096")

	// jellyfinServer1 is the internal URL used by the proxy to reach server 1.
	// When registering backends, we use Docker service names since the proxy
	// runs inside Docker.
	jellyfinServer1 = envOr("E2E_JELLYFIN1_URL", "http://jellyfin-server1:8096")
	jellyfinServer2 = envOr("E2E_JELLYFIN2_URL", "http://jellyfin-server2:8096")

	// jellyfinServer1Direct / 2Direct are the URLs reachable from the test
	// runner (host). Used to complete the Jellyfin startup wizard and add
	// libraries before registering backends with the proxy.
	jellyfinServer1Direct = envOr("E2E_JELLYFIN1_DIRECT_URL", "http://localhost:18196")
	jellyfinServer2Direct = envOr("E2E_JELLYFIN2_DIRECT_URL", "http://localhost:18296")
)

// ── Shared state populated by BeforeSuite ────────────────────────────────────

var (
	adminToken string
	adminUser  userInfo
	userToken  string
	testUser   userInfo
	backend1ID string
	backend2ID string
)

type userInfo struct {
	ID       string
	Username string
}

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
	idtrans.PrewarmMerged()

	By("Setting up Jellyfin server 1")
	setupJellyfin(jellyfinServer1Direct)

	By("Setting up Jellyfin server 2")
	setupJellyfin(jellyfinServer2Direct)

	By("Waiting for proxy to be healthy")
	waitForHealth(proxyBase+"/health", 120*time.Second)

	By("Logging in as admin")
	adminToken = login("admin", "e2e-admin-password")
	Expect(adminToken).NotTo(BeEmpty(), "admin login failed")

	By("Getting admin user info")
	adminUser = getCurrentUser(adminToken)

	By("Registering backend server 1")
	backend1ID = registerBackend("Server 1", jellyfinServer1)

	By("Registering backend server 2")
	backend2ID = registerBackend("Server 2", jellyfinServer2)

	By("Creating a test user")
	testUser = createProxyUser("e2euser", "e2e-test-password!")

	By("Mapping test user to backend server 1")
	loginToBackend(backend1ID, testUser.ID, "root", "password")

	By("Mapping test user to backend server 2")
	loginToBackend(backend2ID, testUser.ID, "root", "password")

	By("Logging in as test user")
	userToken = login("e2euser", "e2e-test-password!")
	Expect(userToken).NotTo(BeEmpty(), "test user login failed")

	By("Setup complete")
})

// ── Bootstrap helpers ────────────────────────────────────────────────────────

func waitForHealth(url string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 3 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			return
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}
	Fail(fmt.Sprintf("proxy did not become healthy at %s within %s", url, timeout))
}

func login(username, password string) string {
	resp := post(proxyBase+"/users/authenticatebyname", map[string]string{
		"Username": username,
		"Pw":       password,
	}, "")
	defer resp.Body.Close()
	ExpectWithOffset(1, resp.StatusCode).To(Equal(http.StatusOK),
		fmt.Sprintf("login failed for %s: status %d", username, resp.StatusCode))

	var body map[string]interface{}
	Expect(json.NewDecoder(resp.Body).Decode(&body)).To(Succeed())
	return body["AccessToken"].(string)
}

func getCurrentUser(token string) userInfo {
	// The /users endpoint returns an array — the authenticated user is in it.
	resp := get(proxyBase+"/users", token)
	defer resp.Body.Close()
	Expect(resp.StatusCode).To(Equal(http.StatusOK))

	var users []map[string]interface{}
	Expect(json.NewDecoder(resp.Body).Decode(&users)).To(Succeed())
	Expect(users).NotTo(BeEmpty())
	return userInfo{
		ID:       users[0]["Id"].(string),
		Username: users[0]["Name"].(string),
	}
}

func registerBackend(name, url string) string {
	resp := post(proxyBase+"/proxy/backends", map[string]interface{}{
		"name": name,
		"url":  url,
	}, adminToken)
	defer resp.Body.Close()
	ExpectWithOffset(1, resp.StatusCode).To(
		SatisfyAny(Equal(http.StatusCreated), Equal(http.StatusConflict)),
		fmt.Sprintf("register backend %s failed: status %d", name, resp.StatusCode))

	if resp.StatusCode == http.StatusConflict {
		// Backend already registered — find it by name.
		list := get(proxyBase+"/proxy/backends", adminToken)
		defer list.Body.Close()
		var backends []map[string]interface{}
		Expect(json.NewDecoder(list.Body).Decode(&backends)).To(Succeed())
		for _, b := range backends {
			if b["name"].(string) == name {
				return b["id"].(string)
			}
		}
		Fail(fmt.Sprintf("backend %s not found after conflict", name))
	}

	var body map[string]interface{}
	Expect(json.NewDecoder(resp.Body).Decode(&body)).To(Succeed())
	return body["id"].(string)
}

func createProxyUser(username, password string) userInfo {
	resp := post(proxyBase+"/proxy/users", map[string]interface{}{
		"username":     username,
		"display_name": username,
		"password":     password,
		"is_admin":     false,
	}, adminToken)
	defer resp.Body.Close()
	ExpectWithOffset(1, resp.StatusCode).To(
		SatisfyAny(Equal(http.StatusCreated), Equal(http.StatusConflict)),
		fmt.Sprintf("create user %s failed: status %d", username, resp.StatusCode))

	if resp.StatusCode == http.StatusConflict {
		// User already exists — find it.
		list := get(proxyBase+"/proxy/users", adminToken)
		defer list.Body.Close()
		var users []map[string]interface{}
		Expect(json.NewDecoder(list.Body).Decode(&users)).To(Succeed())
		for _, u := range users {
			if u["username"].(string) == username {
				return userInfo{ID: u["id"].(string), Username: username}
			}
		}
		Fail(fmt.Sprintf("user %s not found after conflict", username))
	}

	var body map[string]interface{}
	Expect(json.NewDecoder(resp.Body).Decode(&body)).To(Succeed())
	return userInfo{ID: body["id"].(string), Username: username}
}

func loginToBackend(backendID, proxyUserID, jfUser, jfPass string) {
	resp := post(proxyBase+"/proxy/backends/"+backendID+"/login", map[string]interface{}{
		"proxy_user_id": proxyUserID,
		"username":      jfUser,
		"password":      jfPass,
	}, adminToken)
	defer resp.Body.Close()
	ExpectWithOffset(1, resp.StatusCode).To(
		SatisfyAny(Equal(http.StatusCreated), Equal(http.StatusOK)),
		fmt.Sprintf("login to backend %s failed: status %d", backendID, resp.StatusCode))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Jellyfin initial setup ───────────────────────────────────────────────────

// setupJellyfin completes the Jellyfin startup wizard, creates an admin
// account (root / password), adds a Movies library, and waits for the
// library scan to finish. If the server is already set up, this is a no-op.
func setupJellyfin(baseURL string) {
	GinkgoHelper()

	client := &http.Client{Timeout: 10 * time.Second}

	// Wait for Jellyfin to accept HTTP requests.
	waitForHealth(baseURL+"/health", 120*time.Second)

	// Check if already set up — a configured server returns startup
	// config with IsStartupWizardCompleted.
	if jellyfinIsSetUp(client, baseURL) {
		fmt.Fprintf(GinkgoWriter, "Jellyfin at %s already configured, skipping setup\n", baseURL)
		return
	}

	fmt.Fprintf(GinkgoWriter, "Running Jellyfin startup wizard on %s\n", baseURL)

	// 1. Set language / metadata config.
	jellyfinPost(client, baseURL+"/Startup/Configuration", map[string]string{
		"UICulture":                "en-US",
		"MetadataCountryCode":     "US",
		"PreferredMetadataLanguage": "en",
	})

	// 2. GET the startup user first — this triggers Jellyfin to create the
	//    initial user record internally. Without this, the POST (which is
	//    an update) fails with 500.
	jellyfinGet(client, baseURL+"/Startup/User")

	// 3. Set the admin username and password.
	jellyfinPost(client, baseURL+"/Startup/User", map[string]string{
		"Name":     "root",
		"Password": "password",
	})

	// 4. Enable remote access.
	jellyfinPost(client, baseURL+"/Startup/RemoteAccess", map[string]interface{}{
		"EnableRemoteAccess":         true,
		"EnableAutomaticPortMapping": false,
	})

	// 5. Complete the wizard.
	jellyfinPost(client, baseURL+"/Startup/Complete", nil)

	// 6. Authenticate to add libraries.
	token := jellyfinLogin(client, baseURL, "root", "password")

	// 7. Add a Movies library pointing at /media/movies.
	addLibraryURL := baseURL + "/Library/VirtualFolders?" + url.Values{
		"name":           {"Movies"},
		"collectionType": {"movies"},
		"refreshLibrary": {"true"},
		"paths":          {"/media/movies"},
	}.Encode()
	jellyfinPostAuth(client, addLibraryURL, map[string]interface{}{
		"LibraryOptions": map[string]interface{}{},
	}, token)

	// 8. Wait for the library scan to produce items.
	waitForLibraryItems(client, baseURL, token, 90*time.Second)
}

// jellyfinIsSetUp checks whether the startup wizard has already been completed.
func jellyfinIsSetUp(client *http.Client, baseURL string) bool {
	resp, err := client.Get(baseURL + "/System/Info/Public")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var info map[string]interface{}
	if json.Unmarshal(body, &info) != nil {
		return false
	}
	completed, _ := info["StartupWizardCompleted"].(bool)
	return completed
}

func jellyfinGet(client *http.Client, url string) {
	GinkgoHelper()
	resp, err := client.Get(url)
	Expect(err).NotTo(HaveOccurred(), "GET %s", url)
	defer resp.Body.Close()
	Expect(resp.StatusCode).To(Equal(http.StatusOK), "GET %s returned %d", url, resp.StatusCode)
}

func jellyfinPost(client *http.Client, url string, body interface{}) {
	GinkgoHelper()
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, url, bodyReader)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	Expect(err).NotTo(HaveOccurred(), "POST %s", url)
	defer resp.Body.Close()
	Expect(resp.StatusCode).To(SatisfyAny(
		Equal(http.StatusOK),
		Equal(http.StatusNoContent),
		Equal(http.StatusCreated),
	), "POST %s returned %d", url, resp.StatusCode)
}

func jellyfinPostAuth(client *http.Client, url string, body interface{}, token string) {
	GinkgoHelper()
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, url, bodyReader)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", token)
	resp, err := client.Do(req)
	Expect(err).NotTo(HaveOccurred(), "POST %s", url)
	defer resp.Body.Close()
	Expect(resp.StatusCode).To(SatisfyAny(
		Equal(http.StatusOK),
		Equal(http.StatusNoContent),
		Equal(http.StatusCreated),
	), "POST %s returned %d", url, resp.StatusCode)
}

func jellyfinLogin(client *http.Client, baseURL, username, password string) string {
	GinkgoHelper()
	b, _ := json.Marshal(map[string]string{"Username": username, "Pw": password})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/Users/AuthenticateByName", bytes.NewReader(b))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization",
		`MediaBrowser Client="e2e", Device="test", DeviceId="e2e-setup", Version="1.0"`)
	resp, err := client.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	Expect(resp.StatusCode).To(Equal(http.StatusOK), "Jellyfin login failed for %s", username)

	var result map[string]interface{}
	Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
	return result["AccessToken"].(string)
}

// waitForLibraryItems polls the Jellyfin Items endpoint until at least one
// item is returned, indicating the library scan has completed.
func waitForLibraryItems(client *http.Client, baseURL, token string, timeout time.Duration) {
	GinkgoHelper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/Items?Recursive=true&IncludeItemTypes=Movie", nil)
		req.Header.Set("X-Emby-Token", token)
		resp, err := client.Do(req)
		if err == nil {
			var body map[string]interface{}
			if json.NewDecoder(resp.Body).Decode(&body) == nil {
				if total, ok := body["TotalRecordCount"].(float64); ok && total > 0 {
					resp.Body.Close()
					fmt.Fprintf(GinkgoWriter, "Library scan complete: %.0f items\n", total)
					return
				}
			}
			resp.Body.Close()
		}
		time.Sleep(3 * time.Second)
	}
	Fail(fmt.Sprintf("Jellyfin library scan at %s did not complete within %s", baseURL, timeout))
}

