package handler_test

import (
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellyfin-proxy/api/handler"
	"github.com/ddevcap/jellyfin-proxy/config"
	"github.com/ddevcap/jellyfin-proxy/ent"
	"github.com/ddevcap/jellyfin-proxy/idtrans"
)

var _ = Describe("Context helpers", func() {
	Describe("Fallback", func() {
		It("returns the value when non-empty", func() {
			Expect(handler.Fallback("hello", "default")).To(Equal("hello"))
		})

		It("returns the default when empty", func() {
			Expect(handler.Fallback("", "default")).To(Equal("default"))
		})
	})

	Describe("NilIfEmpty", func() {
		It("returns nil for empty string", func() {
			Expect(handler.NilIfEmpty("")).To(BeNil())
		})

		It("returns pointer to string when non-empty", func() {
			result := handler.NilIfEmpty("hello")
			Expect(result).NotTo(BeNil())
			Expect(*result).To(Equal("hello"))
		})
	})

	Describe("ShouldDirectStream", func() {
		It("returns false when user is nil", func() {
			Expect(handler.ShouldDirectStream(nil, "127.0.0.1", config.Config{})).To(BeFalse())
		})

		It("returns false when user has direct_stream=false", func() {
			user := &ent.User{DirectStream: false}
			Expect(handler.ShouldDirectStream(user, "127.0.0.1", config.Config{})).To(BeFalse())
		})

		It("returns true for private IP when user has direct_stream=true", func() {
			user := &ent.User{DirectStream: true}
			Expect(handler.ShouldDirectStream(user, "127.0.0.1", config.Config{})).To(BeTrue())
		})

		It("returns true for 192.168.x.x private IP", func() {
			user := &ent.User{DirectStream: true}
			Expect(handler.ShouldDirectStream(user, "192.168.1.100", config.Config{})).To(BeTrue())
		})

		It("returns false for public IP without custom networks", func() {
			user := &ent.User{DirectStream: true}
			Expect(handler.ShouldDirectStream(user, "8.8.8.8", config.Config{})).To(BeFalse())
		})

		It("returns false for invalid IP", func() {
			user := &ent.User{DirectStream: true}
			Expect(handler.ShouldDirectStream(user, "not-an-ip", config.Config{})).To(BeFalse())
		})
	})

	Describe("RewriteBaseURL", func() {
		It("replaces backend URL with proxy URL", func() {
			result := handler.RewriteBaseURL(
				[]byte("http://backend:8096/videos/abc"),
				"http://backend:8096",
				"http://proxy:8096",
			)
			Expect(string(result)).To(Equal("http://proxy:8096/videos/abc"))
		})

		It("returns body unchanged when URLs match", func() {
			body := []byte("http://same:8096/videos/abc")
			result := handler.RewriteBaseURL(body, "http://same:8096", "http://same:8096")
			Expect(string(result)).To(Equal("http://same:8096/videos/abc"))
		})

		It("returns body unchanged when backend URL is empty", func() {
			body := []byte("some content")
			result := handler.RewriteBaseURL(body, "", "http://proxy:8096")
			Expect(string(result)).To(Equal("some content"))
		})

		It("trims trailing slashes before comparison", func() {
			result := handler.RewriteBaseURL(
				[]byte("http://backend:8096/videos"),
				"http://backend:8096/",
				"http://proxy:8096/",
			)
			Expect(string(result)).To(Equal("http://proxy:8096/videos"))
		})
	})

	Describe("ToUUIDForm", func() {
		It("inserts dashes into a 32-char hex string", func() {
			result := handler.ToUUIDForm("aabbccdd11223344aabbccdd11223344")
			Expect(result).To(Equal("aabbccdd-1122-3344-aabb-ccdd11223344"))
		})

		It("returns the string unchanged if not 32 chars", func() {
			Expect(handler.ToUUIDForm("short")).To(Equal("short"))
		})

		It("returns the string unchanged if not valid hex", func() {
			Expect(handler.ToUUIDForm("gggggggggggggggggggggggggggggggg")).To(Equal("gggggggggggggggggggggggggggggggg"))
		})
	})

	Describe("CollectionTypeToItemType", func() {
		It("maps movies to movie", func() {
			Expect(handler.CollectionTypeToItemType("movies")).To(Equal("movie"))
		})

		It("maps tvshows to series", func() {
			Expect(handler.CollectionTypeToItemType("tvshows")).To(Equal("series"))
		})

		It("returns empty string for unknown type", func() {
			Expect(handler.CollectionTypeToItemType("unknown")).To(BeEmpty())
		})
	})

	Describe("ForwardQuery", func() {
		It("replaces UserId with the backend user ID", func() {
			src := url.Values{"UserId": {"proxy-user-id"}, "Limit": {"10"}}
			dst := handler.ForwardQuery(src, "backend-user-id")
			Expect(dst.Get("UserId")).To(Equal("backend-user-id"))
			Expect(dst.Get("Limit")).To(Equal("10"))
		})

		It("strips ApiKey params", func() {
			src := url.Values{"ApiKey": {"secret"}, "Limit": {"10"}}
			dst := handler.ForwardQuery(src, "buid")
			Expect(dst.Get("ApiKey")).To(BeEmpty())
			Expect(dst.Get("Limit")).To(Equal("10"))
		})

		It("strips apikey (case insensitive) params", func() {
			src := url.Values{"apikey": {"secret"}, "Limit": {"10"}}
			dst := handler.ForwardQuery(src, "buid")
			Expect(dst.Get("apikey")).To(BeEmpty())
			Expect(dst.Get("ApiKey")).To(BeEmpty())
			Expect(dst.Get("Limit")).To(Equal("10"))
		})

		It("decodes ParentId", func() {
			encoded := idtrans.Encode("s1", "parent123")
			src := url.Values{"ParentId": {encoded}}
			dst := handler.ForwardQuery(src, "buid")
			Expect(dst.Get("ParentId")).To(Equal("parent123"))
		})

		It("decodes comma-separated Ids", func() {
			id1 := idtrans.Encode("s1", "item1")
			id2 := idtrans.Encode("s1", "item2")
			src := url.Values{"Ids": {id1 + "," + id2}}
			dst := handler.ForwardQuery(src, "buid")
			Expect(dst.Get("Ids")).To(Equal("item1,item2"))
		})

		It("decodes SeasonId", func() {
			encoded := idtrans.Encode("s1", "season123")
			src := url.Values{"SeasonId": {encoded}}
			dst := handler.ForwardQuery(src, "buid")
			Expect(dst.Get("SeasonId")).To(Equal("season123"))
		})

		It("decodes MediaSourceId", func() {
			encoded := idtrans.Encode("s1", "ms123")
			src := url.Values{"MediaSourceId": {encoded}}
			dst := handler.ForwardQuery(src, "buid")
			Expect(dst.Get("MediaSourceId")).To(Equal("ms123"))
		})

		It("preserves unknown params with canonical casing", func() {
			src := url.Values{"Recursive": {"true"}, "sortBy": {"Name"}}
			dst := handler.ForwardQuery(src, "buid")
			Expect(dst.Get("Recursive")).To(Equal("true"))
		})

		It("omits UserId when backendUserID is empty", func() {
			src := url.Values{"UserId": {"proxy-id"}}
			dst := handler.ForwardQuery(src, "")
			Expect(dst.Get("UserId")).To(BeEmpty())
		})
	})
})

