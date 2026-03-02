package handler_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"

	"github.com/ddevcap/jellymux/api/handler"
	"github.com/ddevcap/jellymux/api/middleware"
	"github.com/ddevcap/jellymux/backend"
	"github.com/ddevcap/jellymux/config"
	"github.com/ddevcap/jellymux/ent"
)

// minimalPNG returns the bytes of a 1×1 red PNG — the smallest valid image.
func minimalPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// avatarRouter wires up an AvatarHandler with the Auth middleware.
// GET /users/:userId/images/primary is public; POST and DELETE are authenticated.
// GET /users/:userId is also mounted so the PrimaryImageTag field can be tested
// through the real JSON response without reaching into handler internals.
func avatarRouter() *gin.Engine {
	cfg := config.Config{ServerID: "test-server-id", ServerName: "Test Proxy"}
	avatarH := handler.NewAvatarHandler(db)
	pool := backend.NewPool(db, cfg)
	mediaH := handler.NewMediaHandler(pool, cfg, db)

	r := gin.New()
	// Public
	r.GET("/users/:userId/images/primary", avatarH.GetAvatar)
	r.GET("/userimage", avatarH.GetAvatarByQuery)

	// Authenticated
	priv := r.Group("/")
	priv.Use(middleware.Auth(db, cfg))
	priv.GET("/users/:userId", mediaH.GetUser)
	priv.POST("/users/:userId/images/primary", avatarH.UploadAvatar)
	priv.DELETE("/users/:userId/images/primary", avatarH.DeleteAvatar)
	return r
}

var _ = Describe("AvatarHandler", func() {
	var (
		router *gin.Engine
		user   *ent.User
		token  string
	)

	BeforeEach(func() {
		cleanDB()
		gin.SetMode(gin.TestMode)
		user = createUser("avatar-user", "password1!", false)
		token = "avatar-test-token"
		createSession(user, token)
		router = avatarRouter()
	})

	authHeader := func() map[string]string {
		return map[string]string{"X-Emby-Token": token}
	}

	// ── GetAvatar ────────────────────────────────────────────────────────────────

	Describe("GetAvatar", func() {
		Context("when the user has no avatar", func() {
			It("returns 404", func() {
				w := doGet(router, "/users/"+user.ID.String()+"/images/primary")
				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("after an avatar has been uploaded", func() {
			It("returns 200 with the image bytes and correct Content-Type", func() {
				img := minimalPNG()
				doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					img, "image/png", authHeader())

				w := doGet(router, "/users/"+user.ID.String()+"/images/primary")

				Expect(w.Code).To(Equal(http.StatusOK))
				Expect(w.Header().Get("Content-Type")).To(ContainSubstring("image/"))
				Expect(w.Body.Bytes()).NotTo(BeEmpty())
			})

			It("is accessible without authentication", func() {
				img := minimalPNG()
				doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					img, "image/png", authHeader())

				// No auth header on GET.
				w := doGet(router, "/users/"+user.ID.String()+"/images/primary")
				Expect(w.Code).To(Equal(http.StatusOK))
			})
		})

		Context("when the user does not exist", func() {
			It("returns 404", func() {
				w := doGet(router, "/users/00000000-0000-0000-0000-000000000001/images/primary")
				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("with a malformed user ID", func() {
			It("returns 400", func() {
				w := doGet(router, "/users/not-a-uuid/images/primary")
				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})

	// ── UploadAvatar ─────────────────────────────────────────────────────────────

	Describe("UploadAvatar", func() {
		Context("with a raw binary PNG body", func() {
			It("returns 204 and stores the avatar", func() {
				img := minimalPNG()
				w := doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					img, "image/png", authHeader())

				Expect(w.Code).To(Equal(http.StatusNoContent))

				get := doGet(router, "/users/"+user.ID.String()+"/images/primary")
				Expect(get.Code).To(Equal(http.StatusOK))
			})
		})

		Context("with a plain base64-encoded body (Jellyfin web UI format)", func() {
			It("returns 204 and stores the decoded avatar", func() {
				encoded := []byte(base64.StdEncoding.EncodeToString(minimalPNG()))
				w := doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					encoded, "text/plain", authHeader())

				Expect(w.Code).To(Equal(http.StatusNoContent))

				get := doGet(router, "/users/"+user.ID.String()+"/images/primary")
				Expect(get.Code).To(Equal(http.StatusOK))
			})
		})

		Context("with a data URL body", func() {
			It("returns 204 and stores the decoded avatar", func() {
				dataURL := []byte("data:image/png;base64," + base64.StdEncoding.EncodeToString(minimalPNG()))
				w := doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					dataURL, "text/plain", authHeader())

				Expect(w.Code).To(Equal(http.StatusNoContent))

				get := doGet(router, "/users/"+user.ID.String()+"/images/primary")
				Expect(get.Code).To(Equal(http.StatusOK))
			})
		})

		Context("with a body larger than 2 MiB", func() {
			It("returns 413", func() {
				big := bytes.Repeat([]byte{0xFF, 0xD8, 0xFF}, (2<<20)/3+1) // fake JPEG magic, >2 MiB
				w := doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					big, "image/jpeg", authHeader())

				Expect(w.Code).To(Equal(http.StatusRequestEntityTooLarge))
			})
		})

		Context("with an empty body", func() {
			It("returns 400", func() {
				w := doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					[]byte{}, "image/png", authHeader())

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("with a non-image content type", func() {
			It("returns 415", func() {
				w := doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					[]byte("hello, world"), "text/plain", authHeader())

				Expect(w.Code).To(Equal(http.StatusUnsupportedMediaType))
			})
		})

		Context("when a non-owner tries to upload to another user's avatar", func() {
			It("returns 403", func() {
				other := createUser("other-user", "password1!", false)
				otherToken := "other-session-token"
				createSession(other, otherToken)

				img := minimalPNG()
				w := doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					img, "image/png",
					map[string]string{"X-Emby-Token": otherToken})

				Expect(w.Code).To(Equal(http.StatusForbidden))
			})
		})

		Context("when an admin uploads to another user's avatar", func() {
			It("returns 204", func() {
				admin := createUser("admin-user", "password1!", true)
				adminToken := "admin-session-token"
				createSession(admin, adminToken)

				img := minimalPNG()
				w := doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					img, "image/png",
					map[string]string{"X-Emby-Token": adminToken})

				Expect(w.Code).To(Equal(http.StatusNoContent))
			})
		})

		Context("without authentication", func() {
			It("returns 401", func() {
				img := minimalPNG()
				w := doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					img, "image/png")

				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	// ── DeleteAvatar ─────────────────────────────────────────────────────────────

	Describe("DeleteAvatar", func() {
		BeforeEach(func() {
			// Upload an avatar first so there is something to delete.
			doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
				minimalPNG(), "image/png", authHeader())
		})

		Context("when the owner deletes their own avatar", func() {
			It("returns 204 and the avatar is gone", func() {
				w := doDelete(router, "/users/"+user.ID.String()+"/images/primary", authHeader())

				Expect(w.Code).To(Equal(http.StatusNoContent))

				get := doGet(router, "/users/"+user.ID.String()+"/images/primary")
				Expect(get.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("when an admin deletes another user's avatar", func() {
			It("returns 204", func() {
				admin := createUser("admin-user", "password1!", true)
				adminToken := "admin-session-token"
				createSession(admin, adminToken)

				w := doDelete(router, "/users/"+user.ID.String()+"/images/primary",
					map[string]string{"X-Emby-Token": adminToken})

				Expect(w.Code).To(Equal(http.StatusNoContent))
			})
		})

		Context("when a non-owner tries to delete another user's avatar", func() {
			It("returns 403", func() {
				other := createUser("other-user", "password1!", false)
				otherToken := "other-session-token"
				createSession(other, otherToken)

				w := doDelete(router, "/users/"+user.ID.String()+"/images/primary",
					map[string]string{"X-Emby-Token": otherToken})

				Expect(w.Code).To(Equal(http.StatusForbidden))
			})
		})

		Context("without authentication", func() {
			It("returns 401", func() {
				w := doDelete(router, "/users/"+user.ID.String()+"/images/primary")
				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	// ── PrimaryImageTag in user object ───────────────────────────────────────────

	Describe("PrimaryImageTag in GET /users/:userId response", func() {
		Context("when the user has no avatar", func() {
			It("is absent from the user object", func() {
				w := doGet(router, "/users/"+user.ID.String(), authHeader())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).NotTo(HaveKey("PrimaryImageTag"))
			})
		})

		Context("after an avatar has been uploaded", func() {
			It("is present and non-empty in the user object", func() {
				doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					minimalPNG(), "image/png", authHeader())

				w := doGet(router, "/users/"+user.ID.String(), authHeader())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).To(HaveKey("PrimaryImageTag"))
				Expect(resp["PrimaryImageTag"]).NotTo(BeEmpty())
			})
		})

		Context("after the avatar has been deleted", func() {
			It("is absent again", func() {
				doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					minimalPNG(), "image/png", authHeader())
				doDelete(router, "/users/"+user.ID.String()+"/images/primary", authHeader())

				w := doGet(router, "/users/"+user.ID.String(), authHeader())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).NotTo(HaveKey("PrimaryImageTag"))
			})
		})
	})

	// ── GetAvatarByQuery ─────────────────────────────────────────────────────────

	Describe("GetAvatarByQuery", func() {
		Context("when userId query param is provided", func() {
			It("returns the user's avatar", func() {
				// Upload avatar first
				doRawPost(router, "/users/"+user.ID.String()+"/images/primary",
					minimalPNG(), "image/png", authHeader())

				w := doGet(router, "/userimage?userId="+user.ID.String())
				Expect(w.Code).To(Equal(http.StatusOK))
				Expect(w.Header().Get("Content-Type")).To(ContainSubstring("image/"))
			})
		})

		Context("when userId is invalid", func() {
			It("returns 400", func() {
				w := doGet(router, "/userimage?userId=not-a-uuid")
				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("when userId refers to a non-existent user", func() {
			It("returns 404", func() {
				w := doGet(router, "/userimage?userId=00000000-0000-0000-0000-000000000001")
				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("when no userId and no authenticated user", func() {
			It("returns 404", func() {
				w := doGet(router, "/userimage")
				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})
	})
})
