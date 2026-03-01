//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Favorites & Played state", func() {

	// getFirstMovieID fetches all merged movies and returns the first proxy-prefixed item ID.
	getFirstMovieID := func() string {
		resp := get(proxyURL("/items?parentId="+idtrans.EncodeMerged("movies")), userToken)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		items, _ := pagedItems(resp)
		Expect(items).NotTo(BeEmpty(), "need at least 1 movie for favorites tests")
		return items[0].(map[string]interface{})["Id"].(string)
	}

	// getUserItemData fetches a single item via the user-scoped endpoint and
	// returns the UserData sub-object.
	getUserItemData := func(itemID string) map[string]interface{} {
		resp := get(proxyURL(fmt.Sprintf("/users/%s/items/%s", testUser.ID, itemID)), userToken)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		body := parseJSONObject(resp)
		ud, ok := body["UserData"].(map[string]interface{})
		Expect(ok).To(BeTrue(), "expected UserData in item response")
		return ud
	}

	Describe("Mark / Unmark Favorite", Ordered, func() {
		var movieID string

		BeforeAll(func() {
			movieID = getFirstMovieID()
		})

		It("marks the item as favorite", func() {
			resp := post(proxyURL(fmt.Sprintf("/users/%s/favoriteitems/%s", testUser.ID, movieID)),
				nil, userToken)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body["IsFavorite"]).To(BeTrue())
		})

		It("the item is now a favorite in UserData", func() {
			ud := getUserItemData(movieID)
			Expect(ud["IsFavorite"]).To(BeTrue())
		})

		It("unmarks the favorite", func() {
			resp := del(proxyURL(fmt.Sprintf("/users/%s/favoriteitems/%s", testUser.ID, movieID)),
				userToken)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body["IsFavorite"]).To(BeFalse())
		})

		It("the item is no longer a favorite", func() {
			ud := getUserItemData(movieID)
			Expect(ud["IsFavorite"]).To(BeFalse())
		})
	})

	Describe("Mark / Unmark Played", Ordered, func() {
		var movieID string

		BeforeAll(func() {
			movieID = getFirstMovieID()
		})

		It("marks the item as played", func() {
			resp := post(proxyURL(fmt.Sprintf("/users/%s/playeditems/%s", testUser.ID, movieID)),
				nil, userToken)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body["Played"]).To(BeTrue())
		})

		It("the item shows as played in UserData", func() {
			ud := getUserItemData(movieID)
			Expect(ud["Played"]).To(BeTrue())
		})

		It("unmarks the played status", func() {
			resp := del(proxyURL(fmt.Sprintf("/users/%s/playeditems/%s", testUser.ID, movieID)),
				userToken)
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body["Played"]).To(BeFalse())
		})

		It("the item is no longer marked as played", func() {
			ud := getUserItemData(movieID)
			Expect(ud["Played"]).To(BeFalse())
		})
	})

	Describe("State persists across sessions", Ordered, func() {
		var movieID string

		BeforeAll(func() {
			movieID = getFirstMovieID()
		})

		It("marks a favorite, then verifies after re-login", func() {
			// Mark favorite.
			resp := post(proxyURL(fmt.Sprintf("/users/%s/favoriteitems/%s", testUser.ID, movieID)),
				nil, userToken)
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			// Re-login.
			newToken := login("e2euser", "e2e-test-password!")

			// Check favorite state with new token.
			resp2 := get(proxyURL(fmt.Sprintf("/users/%s/items/%s", testUser.ID, movieID)), newToken)
			Expect(resp2.StatusCode).To(Equal(http.StatusOK))
			body := parseJSONObject(resp2)
			ud := body["UserData"].(map[string]interface{})
			Expect(ud["IsFavorite"]).To(BeTrue())

			// Clean up: unmark.
			resp3 := del(proxyURL(fmt.Sprintf("/users/%s/favoriteitems/%s", testUser.ID, movieID)),
				newToken)
			resp3.Body.Close()
		})
	})

	Describe("ID translation round-trip", func() {
		It("favorite/played endpoints correctly decode proxy-prefixed IDs", func() {
			movieID := getFirstMovieID()
			prefix := strings.SplitN(movieID, "_", 2)[0]
			Expect(prefix).To(SatisfyAny(Equal("s1"), Equal("s2")),
				"movie ID should have a known server prefix")

			// Mark + unmark should both succeed — proves the proxy decoded the ID correctly.
			resp := post(proxyURL(fmt.Sprintf("/users/%s/favoriteitems/%s", testUser.ID, movieID)),
				nil, userToken)
			resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			resp2 := del(proxyURL(fmt.Sprintf("/users/%s/favoriteitems/%s", testUser.ID, movieID)),
				userToken)
			resp2.Body.Close()
			Expect(resp2.StatusCode).To(Equal(http.StatusOK))
		})
	})
})

