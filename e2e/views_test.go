//go:build e2e

package e2e

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellymux/idtrans"
)

var _ = Describe("Library views", func() {

	Describe("GET /Users/:id/Views (merged views)", func() {
		It("returns merged library views from both backends", func() {
			resp := get(proxyURL("/users/"+testUser.ID+"/views"), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			items := body["Items"].([]interface{})
			Expect(items).NotTo(BeEmpty(), "should have at least one library view")

			// Collect all view IDs and collection types.
			var ids, types []string
			for _, raw := range items {
				item := raw.(map[string]interface{})
				ids = append(ids, item["Id"].(string))
				if ct, ok := item["CollectionType"].(string); ok {
					types = append(types, ct)
				}
			}

			// Both servers have movies, so there should be a merged_movies view.
			Expect(ids).To(ContainElement(idtrans.EncodeMerged("movies")),
				"expected a merged_movies view when both backends have movies libraries")
		})

		It("returns the correct structure for a merged view", func() {
			resp := get(proxyURL("/users/"+testUser.ID+"/views"), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			for _, raw := range body["Items"].([]interface{}) {
				item := raw.(map[string]interface{})
				if item["Id"].(string) == idtrans.EncodeMerged("movies") {
					Expect(item["Name"]).To(Equal("Movies"))
					Expect(item["CollectionType"]).To(Equal("movies"))
					Expect(item["Type"]).To(Equal("CollectionFolder"))
					Expect(item["IsFolder"]).To(BeTrue())
					return
				}
			}
			Fail("merged movies view not found in response")
		})
	})

	Describe("GET /UserViews", func() {
		It("returns the same merged views", func() {
			resp := get(proxyURL("/userviews"), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			items := body["Items"].([]interface{})
			Expect(items).NotTo(BeEmpty())

			var hasMergedMovies bool
			for _, raw := range items {
				item := raw.(map[string]interface{})
				if item["Id"].(string) == idtrans.EncodeMerged("movies") {
					hasMergedMovies = true
					break
				}
			}
			Expect(hasMergedMovies).To(BeTrue())
		})
	})

	Describe("GET /Items/merged_movies (virtual collection)", func() {
		It("returns a synthetic CollectionFolder for the merged view", func() {
			resp := get(proxyURL("/items/"+idtrans.EncodeMerged("movies")), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body["Id"]).To(Equal(idtrans.EncodeMerged("movies")))
			Expect(body["Name"]).To(Equal("Movies"))
			Expect(body["Type"]).To(Equal("CollectionFolder"))
			Expect(body["CollectionType"]).To(Equal("movies"))
			Expect(body["IsFolder"]).To(BeTrue())
		})
	})

	Describe("GET /Users/:id/Items/merged_movies (virtual collection via user path)", func() {
		It("returns a synthetic CollectionFolder for the merged view", func() {
			resp := get(proxyURL("/users/"+testUser.ID+"/items/"+idtrans.EncodeMerged("movies")), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body["Id"]).To(Equal(idtrans.EncodeMerged("movies")))
			Expect(body["Type"]).To(Equal("CollectionFolder"))
		})
	})

	Describe("Item ID format", func() {
		It("all items from browsing a merged library have UUID IDs", func() {
			resp := get(proxyURL("/items?parentId="+idtrans.EncodeMerged("movies")), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			items, total := pagedItems(resp)
			Expect(total).To(BeNumerically(">=", 2),
				"both backends should contribute at least 1 movie each")

			for _, raw := range items {
				item := raw.(map[string]interface{})
				id := item["Id"].(string)
				// Must be a 32-char hex string (dashless UUID).
				Expect(id).To(MatchRegexp(`^[0-9a-f]{32}$`),
					"item ID %q should be a dashless UUID", id)
			}
		})
	})
})
