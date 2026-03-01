package handler_test

import (
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"

	"github.com/ddevcap/jellyfin-proxy/api/handler"
	"github.com/ddevcap/jellyfin-proxy/api/middleware"
	"github.com/ddevcap/jellyfin-proxy/ent"
)

var _ = Describe("ProxyUserHandler", func() {
	var (
		router *gin.Engine
		h      *handler.ProxyUserHandler
	)

	BeforeEach(func() {
		cleanDB()
		gin.SetMode(gin.TestMode)
		h = handler.NewProxyUserHandler(db)
		router = gin.New()
		router.POST("/proxy/users", h.CreateUser)
		router.GET("/proxy/users", h.ListUsers)
		router.GET("/proxy/users/:id", h.GetProxyUser)
		router.GET("/proxy/users/:id/backends", h.GetUserBackends)
		router.PATCH("/proxy/users/:id", h.UpdateUser)
		router.DELETE("/proxy/users/:id", h.DeleteUser)
	})

	// ── CreateUser ────────────────────────────────────────────────────────────

	Describe("CreateUser", func() {
		Context("with valid fields", func() {
			It("returns 201 with the created user (no password in response)", func() {
				w := doPost(router, "/proxy/users", map[string]interface{}{
					"username":     "diana",
					"display_name": "Diana Prince",
					"password":     "securepassword",
					"is_admin":     false,
				})

				Expect(w.Code).To(Equal(http.StatusCreated))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["username"]).To(Equal("diana"))
				Expect(resp["id"]).NotTo(BeEmpty())
				Expect(resp).NotTo(HaveKey("hashed_password"))
				Expect(resp["direct_stream"]).To(BeFalse())
			})
		})

		Context("with direct_stream enabled", func() {
			It("returns 201 with direct_stream set to true", func() {
				w := doPost(router, "/proxy/users", map[string]interface{}{
					"username":      "streamer",
					"display_name":  "Direct Streamer",
					"password":      "securepassword",
					"direct_stream": true,
				})

				Expect(w.Code).To(Equal(http.StatusCreated))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["direct_stream"]).To(BeTrue())
			})
		})

		Context("with a duplicate username", func() {
			It("returns 409", func() {
				createUser("diana", "password1!", false)

				w := doPost(router, "/proxy/users", map[string]interface{}{
					"username":     "diana",
					"display_name": "Diana Again",
					"password":     "password1!",
				})

				Expect(w.Code).To(Equal(http.StatusConflict))
			})
		})

		Context("when the password is too short", func() {
			It("returns 400", func() {
				w := doPost(router, "/proxy/users", map[string]interface{}{
					"username":     "eve",
					"display_name": "Eve",
					"password":     "short",
				})

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("when required fields are missing", func() {
			It("returns 400", func() {
				w := doPost(router, "/proxy/users", map[string]interface{}{
					"username": "frank",
					// display_name and password are missing
				})

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})

	// ── ListUsers ─────────────────────────────────────────────────────────────

	Describe("ListUsers", func() {
		Context("with no users in the database", func() {
			It("returns 200 with an empty array", func() {
				w := doGet(router, "/proxy/users")

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp []interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).To(BeEmpty())
			})
		})

		Context("with multiple users", func() {
			It("returns them sorted ascending by username", func() {
				createUser("zebra", "password1!", false)
				createUser("alpha", "password1!", false)

				w := doGet(router, "/proxy/users")

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp []map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).To(HaveLen(2))
				Expect(resp[0]["username"]).To(Equal("alpha"))
				Expect(resp[1]["username"]).To(Equal("zebra"))
			})
		})
	})

	// ── GetProxyUser ──────────────────────────────────────────────────────────

	Describe("GetProxyUser", func() {
		Context("when the user exists", func() {
			It("returns 200 with the user", func() {
				user := createUser("grace", "password1!", false)

				w := doGet(router, "/proxy/users/"+user.ID.String())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["username"]).To(Equal("grace"))
			})
		})

		Context("when the user does not exist", func() {
			It("returns 404", func() {
				w := doGet(router, "/proxy/users/00000000-0000-0000-0000-000000000001")

				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("with a malformed UUID", func() {
			It("returns 400", func() {
				w := doGet(router, "/proxy/users/not-a-uuid")

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})

	// ── UpdateUser ────────────────────────────────────────────────────────────

	Describe("UpdateUser", func() {
		var user *ent.User

		BeforeEach(func() {
			user = createUser("henry", "password1!", false)
		})

		Context("updating display_name", func() {
			It("returns 200 with the new display_name", func() {
				w := doPatch(router, "/proxy/users/"+user.ID.String(),
					map[string]string{"display_name": "Henry Updated"},
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["display_name"]).To(Equal("Henry Updated"))
			})
		})

		Context("updating is_admin", func() {
			It("returns 200 with is_admin set to true", func() {
				w := doPatch(router, "/proxy/users/"+user.ID.String(),
					map[string]interface{}{"is_admin": true},
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["is_admin"]).To(BeTrue())
			})
		})

		Context("updating direct_stream", func() {
			It("returns 200 with direct_stream toggled on", func() {
				w := doPatch(router, "/proxy/users/"+user.ID.String(),
					map[string]interface{}{"direct_stream": true},
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["direct_stream"]).To(BeTrue())
			})

			It("returns 200 with direct_stream toggled off", func() {
				// First enable it.
				doPatch(router, "/proxy/users/"+user.ID.String(),
					map[string]interface{}{"direct_stream": true},
				)
				// Then disable it.
				w := doPatch(router, "/proxy/users/"+user.ID.String(),
					map[string]interface{}{"direct_stream": false},
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["direct_stream"]).To(BeFalse())
			})
		})

		Context("when no fields are provided", func() {
			It("returns 400", func() {
				w := doPatch(router, "/proxy/users/"+user.ID.String(),
					map[string]interface{}{},
				)

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("when the user does not exist", func() {
			It("returns 404", func() {
				w := doPatch(router, "/proxy/users/00000000-0000-0000-0000-000000000001",
					map[string]string{"display_name": "Ghost"},
				)

				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})
	})

	// ── GetUserBackends ───────────────────────────────────────────────────────

	Describe("GetUserBackends", func() {
		Context("when the user has no mappings", func() {
			It("returns 200 with an empty array", func() {
				user := createUser("alice", "password1!", false)

				w := doGet(router, "/proxy/users/"+user.ID.String()+"/backends")

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp []interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).To(BeEmpty())
			})
		})

		Context("when the user has mappings", func() {
			It("returns 200 with backend details inlined", func() {
				user := createUser("alice", "password1!", false)
				b1 := createBackend("Movies", "http://movies.example.com", "mov")
				b2 := createBackend("TV", "http://tv.example.com", "tv")
				createBackendUser(b1, user, "jf-alice-movies")
				createBackendUser(b2, user, "jf-alice-tv")

				w := doGet(router, "/proxy/users/"+user.ID.String()+"/backends")

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp []map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).To(HaveLen(2))

				backendNames := []string{resp[0]["backend_name"].(string), resp[1]["backend_name"].(string)}
				Expect(backendNames).To(ConsistOf("Movies", "TV"))

				// Spot-check first result has all expected keys.
				r := resp[0]
				Expect(r).To(HaveKey("mapping_id"))
				Expect(r).To(HaveKey("backend_id"))
				Expect(r).To(HaveKey("backend_url"))
				Expect(r).To(HaveKey("prefix"))
				Expect(r).To(HaveKey("backend_user_id"))
				Expect(r).To(HaveKey("enabled"))
			})
		})

		Context("when the user does not exist", func() {
			It("returns 404", func() {
				w := doGet(router, "/proxy/users/00000000-0000-0000-0000-000000000001/backends")

				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("with a malformed UUID", func() {
			It("returns 400", func() {
				w := doGet(router, "/proxy/users/not-a-uuid/backends")

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})

	// ── DeleteUser ────────────────────────────────────────────────────────────

	Describe("DeleteUser", func() {
		Context("when the user exists", func() {
			It("returns 204", func() {
				user := createUser("ivan", "password1!", false)

				w := doDelete(router, "/proxy/users/"+user.ID.String())

				Expect(w.Code).To(Equal(http.StatusNoContent))
			})
		})

		Context("when the user does not exist", func() {
			It("returns 404", func() {
				w := doDelete(router, "/proxy/users/00000000-0000-0000-0000-000000000001")

				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("when an admin attempts to delete their own account", func() {
			It("returns 400", func() {
				admin := createUser("selfdelete", "password1!", true)

				// Build a route that injects the caller into the gin context,
				// replicating what the Auth + AdminOnly middlewares do in production.
				selfRouter := gin.New()
				selfRouter.DELETE("/proxy/users/:id", func(c *gin.Context) {
					c.Set(middleware.ContextKeyUser, admin)
					h.DeleteUser(c)
				})

				w := doDelete(selfRouter, "/proxy/users/"+admin.ID.String())

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})
})
