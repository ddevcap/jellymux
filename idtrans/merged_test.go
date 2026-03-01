package idtrans_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellyfin-proxy/idtrans"
)

var _ = Describe("EncodeMerged", func() {
	It("returns a 32-character dashless UUID string", func() {
		id := idtrans.EncodeMerged("movies")
		Expect(id).To(HaveLen(32))
		// Should not contain dashes.
		Expect(id).NotTo(ContainSubstring("-"))
	})

	It("is deterministic", func() {
		a := idtrans.EncodeMerged("movies")
		b := idtrans.EncodeMerged("movies")
		Expect(a).To(Equal(b))
	})

	It("produces different UUIDs for different collection types", func() {
		Expect(idtrans.EncodeMerged("movies")).NotTo(Equal(idtrans.EncodeMerged("tvshows")))
	})
})

var _ = Describe("DecodeMerged", func() {
	BeforeEach(func() {
		// Ensure the cache is populated.
		idtrans.PrewarmMerged()
	})

	DescribeTable("recognises valid merged IDs",
		func(collectionType string) {
			encoded := idtrans.EncodeMerged(collectionType)
			ct, ok := idtrans.DecodeMerged(encoded)
			Expect(ok).To(BeTrue())
			Expect(ct).To(Equal(collectionType))
		},
		Entry("movies", "movies"),
		Entry("tvshows", "tvshows"),
		Entry("music", "music"),
	)

	DescribeTable("rejects non-merged IDs",
		func(id string) {
			_, ok := idtrans.DecodeMerged(id)
			Expect(ok).To(BeFalse())
		},
		Entry("regular proxy ID", "s1_abc123"),
		Entry("bare ID without prefix", "abc123"),
		Entry("empty string", ""),
		Entry("random UUID", "00000000000000000000000000000001"),
	)

	It("round-trips with EncodeMerged for various content types", func() {
		for _, ct := range []string{"movies", "tvshows", "music", "books", "boxsets"} {
			encoded := idtrans.EncodeMerged(ct)
			decoded, ok := idtrans.DecodeMerged(encoded)
			Expect(ok).To(BeTrue(), "DecodeMerged(%q) returned ok=false", encoded)
			Expect(decoded).To(Equal(ct))
		}
	})

	It("accepts the legacy merged_ prefix format", func() {
		ct, ok := idtrans.DecodeMerged("merged_movies")
		Expect(ok).To(BeTrue())
		Expect(ct).To(Equal("movies"))
	})

	It("rejects merged_ prefix with empty collection type", func() {
		_, ok := idtrans.DecodeMerged("merged_")
		Expect(ok).To(BeFalse())
	})

	It("accepts a dashed UUID form", func() {
		encoded := idtrans.EncodeMerged("tvshows")
		dashed := encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
		ct, ok := idtrans.DecodeMerged(dashed)
		Expect(ok).To(BeTrue())
		Expect(ct).To(Equal("tvshows"))
	})
})
