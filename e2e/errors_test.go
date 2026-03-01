//go:build e2e

package e2e

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellyfin-proxy/idtrans"
)

var _ = Describe("Error handling & edge cases", func() {

	Describe("Invalid tokens", func() {
		It("returns 401 for expired/invalid token on authenticated endpoints", func() {
			endpoints := []string{
				"/system/info",
				"/users/" + testUser.ID + "/views",
				"/items?parentId=" + idtrans.EncodeMerged("movies"),
				"/items/counts",
			}

			for _, ep := range endpoints {
				resp := get(proxyURL(ep), "bogus-token-xyz")
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized),
					"expected 401 for %s with bogus token", ep)
			}
		})

		It("returns 401 for empty token on authenticated endpoints", func() {
			resp := get(proxyURL("/system/info"), "")
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})

	Describe("Invalid item IDs", func() {
		It("returns 400 for unknown item ID", func() {
			resp := get(proxyURL("/items/noprefixhere"), userToken)
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("returns an error for non-existent backend (legacy prefix format)", func() {
			resp := get(proxyURL("/items/zz_nonexistent123"), userToken)
			resp.Body.Close()
			Expect(resp.StatusCode).To(SatisfyAny(
				Equal(http.StatusBadRequest),
				Equal(http.StatusNotFound),
			))
		})

		It("returns 400 for download with unknown ID", func() {
			resp := get(proxyURL("/items/badid/download?api_key="+userToken), "")
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})
	})

	Describe("Public endpoints work without auth", func() {
		It("GET /System/Info/Public returns server info", func() {
			resp := get(proxyURL("/system/info/public"), "")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body["Id"]).To(Equal("e2eproxyserverid"))
			Expect(body["ServerName"]).To(Equal("E2E Proxy"))
			Expect(body).To(HaveKey("Version"))
		})

		It("GET /Branding/Configuration returns branding", func() {
			resp := get(proxyURL("/branding/configuration"), "")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			body := parseJSONObject(resp)
			Expect(body).To(HaveKey("CustomCss"))
		})

		It("GET /QuickConnect/Enabled returns false", func() {
			resp := get(proxyURL("/quickconnect/enabled"), "")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
		})

		It("GET /Health returns ok", func() {
			resp := get(proxyURL("/health"), "")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			body := parseJSONObject(resp)
			Expect(body["status"]).To(Equal("ok"))
		})

		It("GET /Ready returns ready", func() {
			resp := get(proxyURL("/ready"), "")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			body := parseJSONObject(resp)
			Expect(body["status"]).To(Equal("ready"))
		})
	})

	Describe("Static stub endpoints", func() {
		It("GET /SyncPlay/List returns empty array", func() {
			resp := get(proxyURL("/syncplay/list"), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			items := parseJSONArray(resp)
			Expect(items).To(BeEmpty())
		})

		It("GET /Sessions returns empty array", func() {
			resp := get(proxyURL("/sessions"), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			items := parseJSONArray(resp)
			Expect(items).To(BeEmpty())
		})

		It("GET /Notifications/Summary returns zero unread", func() {
			resp := get(proxyURL("/notifications/summary"), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			body := parseJSONObject(resp)
			Expect(body["UnreadCount"]).To(BeNumerically("==", 0))
		})
	})

	Describe("404 for unknown endpoints", func() {
		It("returns 404 for completely unknown paths", func() {
			resp := get(proxyURL("/totally/unknown/endpoint"), userToken)
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})
	})

	Describe("Proxy admin API requires admin role", func() {
		It("non-admin user gets 403 on /proxy/backends", func() {
			resp := get(proxyURL("/proxy/backends"), userToken)
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		})

		It("non-admin user gets 403 on /proxy/users", func() {
			resp := get(proxyURL("/proxy/users"), userToken)
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		})

		It("admin user can access /proxy/backends", func() {
			resp := get(proxyURL("/proxy/backends"), adminToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			items := parseJSONArray(resp)
			Expect(len(items)).To(BeNumerically(">=", 2))
		})
	})
})
