//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellyfin-proxy/idtrans"
)

var _ = Describe("Playback", func() {

	// getFirstMovieID fetches all merged movies and returns the first proxy item ID.
	getFirstMovieID := func() string {
		resp := get(proxyURL("/items?parentId="+idtrans.EncodeMerged("movies")), userToken)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		items, _ := pagedItems(resp)
		Expect(items).NotTo(BeEmpty(), "need at least 1 movie for playback tests")
		return items[0].(map[string]interface{})["Id"].(string)
	}

	Describe("GET /Items/:id/PlaybackInfo", func() {
		It("returns rewritten MediaSources with proxy IDs", func() {
			movieID := getFirstMovieID()

			resp := get(proxyURL(fmt.Sprintf("/items/%s/playbackinfo", movieID)), userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			Expect(body).To(HaveKey("MediaSources"))

			sources := body["MediaSources"].([]interface{})
			Expect(sources).NotTo(BeEmpty(), "expected at least one media source")

			source := sources[0].(map[string]interface{})
			sourceID := source["Id"].(string)
			// The Id should be a 32-char dashless UUID (proxy-rewritten).
			Expect(sourceID).To(MatchRegexp(`^[0-9a-f]{32}$`),
				"MediaSource Id should be a 32-char hex proxy UUID")
		})
	})

	Describe("POST /Items/:id/PlaybackInfo", func() {
		It("returns rewritten TranscodingUrl with proxy session token", func() {
			movieID := getFirstMovieID()

			resp := post(proxyURL(fmt.Sprintf("/items/%s/playbackinfo", movieID)),
				map[string]interface{}{
					"DeviceProfile": map[string]interface{}{},
				}, userToken)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body := parseJSONObject(resp)
			sources := body["MediaSources"].([]interface{})
			Expect(sources).NotTo(BeEmpty())

			source := sources[0].(map[string]interface{})

			// Check TranscodingUrl if present (only exists when transcoding is needed).
			if tu, ok := source["TranscodingUrl"].(string); ok && tu != "" {
				Expect(tu).To(ContainSubstring(movieID),
					"TranscodingUrl should reference the proxy-prefixed movie ID")
				Expect(tu).To(ContainSubstring("ApiKey="),
					"TranscodingUrl should contain the proxy session ApiKey")
			}

			// Check DirectStreamUrl if present.
			if ds, ok := source["DirectStreamUrl"].(string); ok && ds != "" {
				Expect(ds).To(ContainSubstring(movieID),
					"DirectStreamUrl should reference the proxy-prefixed movie ID")
			}
		})
	})

	Describe("GET /Items/:id/Download (proxy mode)", func() {
		It("streams the file through the proxy", func() {
			movieID := getFirstMovieID()

			resp := get(proxyURL(fmt.Sprintf("/items/%s/download", movieID))+
				"?api_key="+userToken, "")
			defer resp.Body.Close()

			// Should get the actual file, not a redirect (user.direct_stream defaults to false).
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(resp.Header.Get("Content-Type")).To(SatisfyAny(
				ContainSubstring("video/"),
				ContainSubstring("application/octet-stream"),
			))

			// Read first few bytes to confirm it's actual data.
			first := make([]byte, 16)
			n, _ := io.ReadAtLeast(resp.Body, first, 4)
			Expect(n).To(BeNumerically(">=", 4), "expected at least a few bytes of video data")
		})
	})

	Describe("GET /Items/:id/Images/Primary", func() {
		It("proxies the image from the backend", func() {
			movieID := getFirstMovieID()

			resp := get(proxyURL(fmt.Sprintf("/items/%s/images/primary", movieID)), "")
			defer resp.Body.Close()

			// Images may or may not exist depending on metadata scan.
			// Accept 200 (found) or 404 (no image).
			Expect(resp.StatusCode).To(SatisfyAny(
				Equal(http.StatusOK),
				Equal(http.StatusNotFound),
			))

			if resp.StatusCode == http.StatusOK {
				Expect(resp.Header.Get("Content-Type")).To(ContainSubstring("image/"))
			}
		})
	})
})

