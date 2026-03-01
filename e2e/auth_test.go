//go:build e2e

package e2e

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Authentication", func() {

	Describe("Login", func() {
		It("returns a token for valid credentials", func() {
			resp := post(proxyURL("/users/authenticatebyname"), map[string]string{
				"Username": "e2euser",
				"Pw":       "e2e-test-password!",
			}, "")
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body).To(HaveKey("AccessToken"))
			Expect(body["AccessToken"]).NotTo(BeEmpty())
			Expect(body).To(HaveKey("User"))
		})

		It("returns 401 for wrong password", func() {
			resp := post(proxyURL("/users/authenticatebyname"), map[string]string{
				"Username": "e2euser",
				"Pw":       "wrong-password",
			}, "")
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("returns 401 for non-existent user", func() {
			resp := post(proxyURL("/users/authenticatebyname"), map[string]string{
				"Username": "ghost",
				"Pw":       "password",
			}, "")
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})

	Describe("Authenticated requests", func() {
		It("succeeds with a valid token", func() {
			resp := get(proxyURL("/system/info"), userToken)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body["Id"]).To(Equal("e2eproxyserverid"))
			Expect(body["ServerName"]).To(Equal("E2E Proxy"))
		})

		It("returns 401 without a token", func() {
			resp := get(proxyURL("/system/info"), "")
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("returns 401 with an invalid token", func() {
			resp := get(proxyURL("/system/info"), "invalid-token-123")
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})

	Describe("Logout", func() {
		It("invalidates the session token", func() {
			// Create a fresh login to get a disposable token.
			token := login("e2euser", "e2e-test-password!")

			// Verify the token works.
			resp := get(proxyURL("/system/info"), token)
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			// Logout.
			logoutResp := post(proxyURL("/sessions/logout"), nil, token)
			logoutResp.Body.Close()
			Expect(logoutResp.StatusCode).To(SatisfyAny(
				Equal(http.StatusOK),
				Equal(http.StatusNoContent),
			))

			// Token should now be rejected.
			resp2 := get(proxyURL("/system/info"), token)
			resp2.Body.Close()
			Expect(resp2.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})
})
