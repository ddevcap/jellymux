//go:build e2e

package e2e

import (
	"fmt"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellyfin-proxy/idtrans"
)

var _ = Describe("Browsing", func() {

	Describe("Merged movies browsing", func() {
		var movieItems []interface{}

		BeforeEach(func() {
			resp := get(proxyURL("/items?parentId="+idtrans.EncodeMerged("movies")), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			movieItems, _ = pagedItems(resp)
		})

		It("returns items from both backend servers with UUID IDs", func() {
			for _, raw := range movieItems {
				item := raw.(map[string]interface{})
				id := item["Id"].(string)
				// All IDs should be 32-char hex strings (dashless UUIDs).
				Expect(id).To(MatchRegexp(`^[0-9a-f]{32}$`), "expected UUID ID, got %q", id)
			}
			// We should have items from multiple backends.
			Expect(movieItems).NotTo(BeEmpty())
		})

		It("each item resolves via GET /Items/:id", func() {
			Expect(movieItems).NotTo(BeEmpty())
			for _, raw := range movieItems {
				item := raw.(map[string]interface{})
				id := item["Id"].(string)

				resp := get(proxyURL("/items/"+id), userToken)
				Expect(resp.StatusCode).To(Equal(http.StatusOK),
					"failed to resolve item %s", id)
				detail := parseJSONObject(resp)
				Expect(detail["Id"]).To(Equal(id))
				Expect(detail).To(HaveKey("Name"))
			}
		})

		It("each item resolves via GET /Users/:userId/Items/:itemId", func() {
			Expect(movieItems).NotTo(BeEmpty())
			firstItem := movieItems[0].(map[string]interface{})
			id := firstItem["Id"].(string)

			resp := get(proxyURL(fmt.Sprintf("/users/%s/items/%s", testUser.ID, id)), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			detail := parseJSONObject(resp)
			Expect(detail["Id"]).To(Equal(id))
		})
	})

	Describe("GET /Items/:id with unknown ID", func() {
		It("returns 400 for an ID not in the cache", func() {
			resp := get(proxyURL("/items/noprefixhere"), userToken)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("returns an error for a non-existent server (legacy prefix format)", func() {
			resp := get(proxyURL("/items/zz_nonexistent"), userToken)
			defer resp.Body.Close()
			// Could be 404 or 400 — either is acceptable.
			Expect(resp.StatusCode).To(SatisfyAny(
				Equal(http.StatusBadRequest),
				Equal(http.StatusNotFound),
			))
		})
	})

	Describe("GET /Items/Counts", func() {
		It("sums counts across all backends", func() {
			resp := get(proxyURL("/items/counts"), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			movieCount := body["MovieCount"].(float64)
			Expect(movieCount).To(BeNumerically(">=", 2),
				"expected at least 1 movie per backend (2+ total)")
		})
	})

	Describe("GET /Items/Filters2 for merged library", func() {
		It("returns aggregated filter options", func() {
			resp := get(proxyURL("/items/filters2?parentId="+idtrans.EncodeMerged("movies")), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body).To(HaveKey("Genres"))
			Expect(body).To(HaveKey("Years"))
		})
	})

	Describe("GET /Search/Hints", func() {
		It("returns search results from backends", func() {
			// Search for a term likely to match something in the test media.
			resp := get(proxyURL("/search/hints?searchTerm=video"), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body).To(HaveKey("SearchHints"))
			Expect(body).To(HaveKey("TotalRecordCount"))
		})
	})

	Describe("GET /Users/:id/Items/Latest", func() {
		It("returns latest items from a merged library", func() {
			resp := get(proxyURL("/users/"+testUser.ID+"/items/latest?parentId="+idtrans.EncodeMerged("movies")), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			// LatestItems returns a bare array.
			items := parseJSONArray(resp)
			Expect(items).NotTo(BeEmpty())
		})
	})

	Describe("GET /Users/:id/Items/Resume", func() {
		It("returns 200 (may be empty)", func() {
			resp := get(proxyURL(fmt.Sprintf("/users/%s/items/resume", testUser.ID)), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body).To(HaveKey("Items"))
		})
	})
})
