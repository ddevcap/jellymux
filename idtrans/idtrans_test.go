package idtrans_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellyfin-proxy/idtrans"
)

type obj = map[string]interface{}

// rewriteResponse is a test helper that marshals input, runs RewriteResponse,
// and returns the unmarshalled result. Expectations are inline so Ginkgo
// reports the correct spec location on failure.
func rewriteResponse(input obj, prefix, proxyServerID string) obj { //nolint:unparam
	return rewriteResponseWithBackend(input, prefix, proxyServerID, nil)
}

func rewriteResponseWithBackend(input obj, prefix, proxyServerID string, backend *idtrans.BackendInfo) obj {
	b, err := json.Marshal(input)
	Expect(err).NotTo(HaveOccurred())
	result, err := idtrans.RewriteResponse(b, prefix, proxyServerID, backend)
	Expect(err).NotTo(HaveOccurred())
	var out obj
	Expect(json.Unmarshal(result, &out)).To(Succeed())
	return out
}

// rewriteRequest is a test helper that marshals input, runs RewriteRequest,
// and returns the unmarshalled result.
func rewriteRequest(input obj) obj {
	b, err := json.Marshal(input)
	Expect(err).NotTo(HaveOccurred())
	result, err := idtrans.RewriteRequest(b)
	Expect(err).NotTo(HaveOccurred())
	var out obj
	Expect(json.Unmarshal(result, &out)).To(Succeed())
	return out
}

// ── Encode ────────────────────────────────────────────────────────────────────

var _ = Describe("Encode", func() {
	It("returns a 32-char lowercase hex string (dashless UUID)", func() {
		result := idtrans.Encode("s1", "abc123")
		Expect(result).To(HaveLen(32))
		Expect(result).To(MatchRegexp(`^[0-9a-f]{32}$`))
	})

	It("returns an empty string when backendID is empty", func() {
		Expect(idtrans.Encode("s1", "")).To(BeEmpty())
	})

	It("produces different IDs for different servers with the same backendID", func() {
		a := idtrans.Encode("server1", "abc123")
		b := idtrans.Encode("server2", "abc123")
		Expect(a).NotTo(Equal(b))
	})

	It("is deterministic (same inputs → same output)", func() {
		a := idtrans.Encode("s1", "abc123")
		b := idtrans.Encode("s1", "abc123")
		Expect(a).To(Equal(b))
	})
})

// ── Decode ────────────────────────────────────────────────────────────────────

var _ = Describe("Decode", func() {
	Context("legacy prefix_backendID format", func() {
		DescribeTable("correctly splits prefix and backendID",
			func(proxyID, wantPrefix, wantBackendID string) {
				prefix, backendID, err := idtrans.Decode(proxyID)
				Expect(err).NotTo(HaveOccurred())
				Expect(prefix).To(Equal(wantPrefix))
				Expect(backendID).To(Equal(wantBackendID))
			},
			Entry("simple alphanumeric ID", "s1_abc123", "s1", "abc123"),
			Entry("UUID with hyphens as backendID", "s2_a1b2c3d4-e5f6-7890-abcd-ef1234567890", "s2", "a1b2c3d4-e5f6-7890-abcd-ef1234567890"),
			Entry("backendID that itself contains underscores", "s1_has_underscore", "s1", "has_underscore"),
		)
	})

	Context("when the ID is not in cache and has no underscore", func() {
		It("returns an error", func() {
			_, _, err := idtrans.Decode("noprefixhere")
			Expect(err).To(HaveOccurred())
		})

		It("returns the original value as backendID so callers can pass it through", func() {
			_, backendID, _ := idtrans.Decode("noprefixhere")
			Expect(backendID).To(Equal("noprefixhere"))
		})
	})

	It("round-trips with Encode", func() {
		serverID, backendID := "server-abc", "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
		encoded := idtrans.Encode(serverID, backendID)
		gotServer, gotBackend, err := idtrans.Decode(encoded)
		Expect(err).NotTo(HaveOccurred())
		Expect(gotServer).To(Equal(serverID))
		Expect(gotBackend).To(Equal(backendID))
	})
})

// ── RewriteResponse ───────────────────────────────────────────────────────────

var _ = Describe("RewriteResponse", func() {
	Context("top-level ID fields", func() {
		It("encodes Id and ParentId as UUIDs, replaces ServerId, leaves non-ID fields unchanged", func() {
			out := rewriteResponse(obj{
				"Id":       "abc123",
				"ParentId": "def456",
				"ServerId": "backend-server-uuid",
				"Name":     "My Movie",
			}, "s1", "proxy-server-id")

			Expect(out["Id"]).To(Equal(idtrans.Encode("s1", "abc123")))
			Expect(out["ParentId"]).To(Equal(idtrans.Encode("s1", "def456")))
			Expect(out["ServerId"]).To(Equal("proxy-server-id"))
			Expect(out["Name"]).To(Equal("My Movie"))
		})
	})

	Context("nested Items array", func() {
		It("rewrites Id and ServerId in each item", func() {
			out := rewriteResponse(obj{
				"Id": "parent",
				"Items": []interface{}{
					obj{"Id": "child1", "ServerId": "backend-id", "Name": "Episode 1"},
					obj{"Id": "child2", "ServerId": "backend-id", "Name": "Episode 2"},
				},
			}, "s1", "proxy-id")

			Expect(out["Id"]).To(Equal(idtrans.Encode("s1", "parent")))
			items := out["Items"].([]interface{})
			item0 := items[0].(obj)
			item1 := items[1].(obj)
			Expect(item0["Id"]).To(Equal(idtrans.Encode("s1", "child1")))
			Expect(item0["ServerId"]).To(Equal("proxy-id"))
			Expect(item1["Id"]).To(Equal(idtrans.Encode("s1", "child2")))
			Expect(item1["ServerId"]).To(Equal("proxy-id"))
		})
	})

	Context("UserData sub-object", func() {
		It("rewrites ItemId inside UserData", func() {
			out := rewriteResponse(obj{
				"Id": "abc123",
				"UserData": obj{
					"ItemId":     "abc123",
					"Played":     false,
					"IsFavorite": false,
				},
			}, "s1", "proxy-id")

			Expect(out["Id"]).To(Equal(idtrans.Encode("s1", "abc123")))
			userData := out["UserData"].(obj)
			Expect(userData["ItemId"]).To(Equal(idtrans.Encode("s1", "abc123")))
		})
	})

	Context("ArtistItems array", func() {
		It("rewrites Id inside each ArtistItem", func() {
			out := rewriteResponse(obj{
				"Id": "album1",
				"ArtistItems": []interface{}{
					obj{"Id": "artist1", "Name": "Artist One"},
				},
			}, "s1", "proxy-id")

			Expect(out["Id"]).To(Equal(idtrans.Encode("s1", "album1")))
			artists := out["ArtistItems"].([]interface{})
			Expect(artists[0].(obj)["Id"]).To(Equal(idtrans.Encode("s1", "artist1")))
		})
	})

	Context("empty string IDs", func() {
		It("leaves empty ID fields unchanged", func() {
			out := rewriteResponse(obj{"Id": "abc", "ParentId": ""}, "s1", "proxy-id")

			Expect(out["Id"]).To(Equal(idtrans.Encode("s1", "abc")))
			Expect(out["ParentId"]).To(Equal(""))
		})
	})

	Context("null IDs", func() {
		It("passes null fields through unchanged", func() {
			raw := []byte(`{"Id":"abc","SeriesId":null}`)
			result, err := idtrans.RewriteResponse(raw, "s1", "proxy-id", nil)
			Expect(err).NotTo(HaveOccurred())
			var out obj
			Expect(json.Unmarshal(result, &out)).To(Succeed())
			Expect(out["Id"]).To(Equal(idtrans.Encode("s1", "abc")))
			Expect(out["SeriesId"]).To(BeNil())
		})
	})

	Context("backend info injection", func() {
		bi := &idtrans.BackendInfo{ID: "be-uuid", Name: "My NAS", URL: "http://nas:8096"}

		It("injects BackendId, BackendName, BackendUrl on objects with an Id field", func() {
			out := rewriteResponseWithBackend(obj{
				"Id":   "abc123",
				"Name": "My Movie",
			}, "s1", "proxy-id", bi)

			Expect(out["BackendId"]).To(Equal("be-uuid"))
			Expect(out["BackendName"]).To(Equal("My NAS"))
			Expect(out["BackendUrl"]).To(Equal("http://nas:8096"))
		})

		It("injects backend info into nested Items array objects", func() {
			out := rewriteResponseWithBackend(obj{
				"Id": "parent",
				"Items": []interface{}{
					obj{"Id": "child1", "Name": "Episode 1"},
					obj{"Id": "child2", "Name": "Episode 2"},
				},
			}, "s1", "proxy-id", bi)

			items := out["Items"].([]interface{})
			for _, item := range items {
				m := item.(obj)
				Expect(m["BackendId"]).To(Equal("be-uuid"))
				Expect(m["BackendName"]).To(Equal("My NAS"))
				Expect(m["BackendUrl"]).To(Equal("http://nas:8096"))
			}
			// Parent also has Id, so it should get backend info too.
			Expect(out["BackendId"]).To(Equal("be-uuid"))
		})

		It("does not inject backend info on objects without an Id field", func() {
			out := rewriteResponseWithBackend(obj{
				"Name": "No Id here",
			}, "s1", "proxy-id", bi)

			Expect(out).NotTo(HaveKey("BackendId"))
			Expect(out).NotTo(HaveKey("BackendName"))
			Expect(out).NotTo(HaveKey("BackendUrl"))
		})

		It("does not inject backend info when BackendInfo is nil", func() {
			out := rewriteResponseWithBackend(obj{
				"Id":   "abc123",
				"Name": "My Movie",
			}, "s1", "proxy-id", nil)

			Expect(out).NotTo(HaveKey("BackendId"))
			Expect(out).NotTo(HaveKey("BackendName"))
			Expect(out).NotTo(HaveKey("BackendUrl"))
		})
	})
})

// ── RewriteRequest ────────────────────────────────────────────────────────────

var _ = Describe("RewriteRequest", func() {
	Context("proxied IDs (UUID format via Encode)", func() {
		It("strips the encoding, leaving non-ID fields untouched", func() {
			encoded := idtrans.Encode("s1", "abc123")
			out := rewriteRequest(obj{
				"ItemId":    encoded,
				"SomeOther": "untouched",
			})

			Expect(out["ItemId"]).To(Equal("abc123"))
			Expect(out["SomeOther"]).To(Equal("untouched"))
		})

		It("leaves ServerId unchanged", func() {
			encoded := idtrans.Encode("s1", "abc")
			out := rewriteRequest(obj{"Id": encoded, "ServerId": "proxy-server-id"})

			Expect(out["Id"]).To(Equal("abc"))
			Expect(out["ServerId"]).To(Equal("proxy-server-id"))
		})
	})

	Context("legacy prefix_backendID format", func() {
		It("strips the prefix for backward compatibility", func() {
			out := rewriteRequest(obj{"ItemId": "s1_abc123"})
			Expect(out["ItemId"]).To(Equal("abc123"))
		})
	})

	Context("non-proxied IDs (unknown format)", func() {
		It("passes the ID through unchanged", func() {
			out := rewriteRequest(obj{"Id": "noprefixhere"})

			Expect(out["Id"]).To(Equal("noprefixhere"))
		})
	})
})
