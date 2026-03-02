package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"

	"github.com/ddevcap/jellymux/api/handler"
	"github.com/ddevcap/jellymux/ent"
)

var _ = Describe("BackendHandler", func() {
	var (
		router *gin.Engine
		h      *handler.BackendHandler
	)

	BeforeEach(func() {
		cleanDB()
		gin.SetMode(gin.TestMode)
		h = handler.NewBackendHandler(db)
		router = gin.New()
		router.POST("/proxy/backends", h.CreateBackend)
		router.GET("/proxy/backends", h.ListBackends)
		router.GET("/proxy/backends/:id", h.GetBackend)
		router.PATCH("/proxy/backends/:id", h.UpdateBackend)
		router.DELETE("/proxy/backends/:id", h.DeleteBackend)
		router.POST("/proxy/backends/:id/users", h.CreateBackendUser)
		router.GET("/proxy/backends/:id/users", h.ListBackendUsers)
		router.PATCH("/proxy/backends/:id/users/:mappingId", h.UpdateBackendUser)
		router.DELETE("/proxy/backends/:id/users/:mappingId", h.DeleteBackendUser)
		router.POST("/proxy/backends/:id/login", h.LoginToBackend)
	})

	// ── CreateBackend ────────────────────────────────────────────────────────────

	Describe("CreateBackend", func() {
		// backendMock starts a fake Jellyfin server that serves /system/info/public.
		backendMock := func(serverID string) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/system/info/public":
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"Id": serverID,
					})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
		}

		Context("with valid request", func() {
			It("returns 201 with the backend (token omitted from response)", func() {
				mock := backendMock("server-uuid-1")
				defer mock.Close()

				w := doPost(router, "/proxy/backends", map[string]interface{}{
					"name": "Primary",
					"url":  mock.URL,
				})

				Expect(w.Code).To(Equal(http.StatusCreated))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["name"]).To(Equal("Primary"))
				Expect(resp["external_id"]).To(Equal("server-uuid-1"))
				Expect(resp).NotTo(HaveKey("token"))
			})
		})

		Context("when the backend is unreachable", func() {
			It("returns 502", func() {
				w := doPost(router, "/proxy/backends", map[string]interface{}{
					"name": "Primary",
					"url":  "http://127.0.0.1:1", // nothing listening
				})

				Expect(w.Code).To(Equal(http.StatusBadGateway))
			})
		})

		Context("with a duplicate external_id", func() {
			It("returns 409", func() {
				// First backend is created with server ID "s1" via createBackend.
				createBackend("Primary", "http://a.example.com", "server-uuid-dup")

				// Mock returns the SAME server ID — should conflict.
				mock := backendMock("server-uuid-dup")
				defer mock.Close()

				w := doPost(router, "/proxy/backends", map[string]interface{}{
					"name": "Secondary",
					"url":  mock.URL,
				})

				Expect(w.Code).To(Equal(http.StatusConflict))
			})
		})

		Context("when required fields are missing", func() {
			It("returns 400", func() {
				w := doPost(router, "/proxy/backends", map[string]interface{}{
					"name": "Incomplete",
				})

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})

	// ── ListBackends ─────────────────────────────────────────────────────────────

	Describe("ListBackends", func() {
		Context("with no backends", func() {
			It("returns 200 with an empty array", func() {
				w := doGet(router, "/proxy/backends")

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp []interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).To(BeEmpty())
			})
		})

		Context("with multiple backends", func() {
			It("returns them sorted ascending by name", func() {
				createBackend("Zebra", "http://z.example.com", "sz")
				createBackend("Alpha", "http://a.example.com", "sa")

				w := doGet(router, "/proxy/backends")

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp []map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).To(HaveLen(2))
				Expect(resp[0]["name"]).To(Equal("Alpha"))
				Expect(resp[1]["name"]).To(Equal("Zebra"))
			})
		})
	})

	// ── GetBackend ───────────────────────────────────────────────────────────────

	Describe("GetBackend", func() {
		Context("when the backend exists", func() {
			It("returns 200 with the backend", func() {
				backend := createBackend("Primary", "http://media.example.com", "s1")

				w := doGet(router, "/proxy/backends/"+backend.ID.String())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["external_id"]).To(Equal("s1"))
			})
		})

		Context("when the backend does not exist", func() {
			It("returns 404", func() {
				w := doGet(router, "/proxy/backends/00000000-0000-0000-0000-000000000001")

				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("with a malformed UUID", func() {
			It("returns 400", func() {
				w := doGet(router, "/proxy/backends/not-a-uuid")

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})

	// ── UpdateBackend ────────────────────────────────────────────────────────────

	Describe("UpdateBackend", func() {
		var backend *ent.Backend

		BeforeEach(func() {
			backend = createBackend("Primary", "http://old.example.com", "s1")
		})

		Context("updating name", func() {
			It("returns 200 with the new name", func() {
				w := doPatch(router, "/proxy/backends/"+backend.ID.String(),
					map[string]string{"name": "Renamed"},
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["name"]).To(Equal("Renamed"))
			})
		})

		Context("toggling enabled to false", func() {
			It("returns 200 with enabled=false", func() {
				w := doPatch(router, "/proxy/backends/"+backend.ID.String(),
					map[string]interface{}{"enabled": false},
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["enabled"]).To(BeFalse())
			})
		})

		Context("when no fields are provided", func() {
			It("returns 400", func() {
				w := doPatch(router, "/proxy/backends/"+backend.ID.String(),
					map[string]interface{}{},
				)

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("when the backend does not exist", func() {
			It("returns 404", func() {
				w := doPatch(router, "/proxy/backends/00000000-0000-0000-0000-000000000001",
					map[string]string{"name": "Ghost"},
				)

				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})
	})

	// ── DeleteBackend ────────────────────────────────────────────────────────────

	Describe("DeleteBackend", func() {
		Context("when the backend exists", func() {
			It("returns 204", func() {
				backend := createBackend("Primary", "http://media.example.com", "s1")

				w := doDelete(router, "/proxy/backends/"+backend.ID.String())

				Expect(w.Code).To(Equal(http.StatusNoContent))
			})
		})

		Context("when the backend does not exist", func() {
			It("returns 404", func() {
				w := doDelete(router, "/proxy/backends/00000000-0000-0000-0000-000000000001")

				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("with a malformed UUID", func() {
			It("returns 400", func() {
				w := doDelete(router, "/proxy/backends/not-a-uuid")

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})

	// ── CreateBackendUser ────────────────────────────────────────────────────────

	Describe("CreateBackendUser", func() {
		var (
			backend *ent.Backend
			user    *ent.User
		)

		BeforeEach(func() {
			backend = createBackend("Primary", "http://media.example.com", "s1")
			user = createUser("alice", "password1!", false)
		})

		Context("with valid fields", func() {
			It("returns 201 with the mapping", func() {
				w := doPost(router, "/proxy/backends/"+backend.ID.String()+"/users",
					map[string]interface{}{
						"user_id":         user.ID.String(),
						"backend_user_id": "jellyfin-user-abc",
					},
				)

				Expect(w.Code).To(Equal(http.StatusCreated))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["backend_user_id"]).To(Equal("jellyfin-user-abc"))
				Expect(resp["username"]).To(Equal("alice"))
			})
		})

		Context("when the mapping already exists", func() {
			It("returns 409", func() {
				createBackendUser(backend, user, "jellyfin-user-abc")

				w := doPost(router, "/proxy/backends/"+backend.ID.String()+"/users",
					map[string]interface{}{
						"user_id":         user.ID.String(),
						"backend_user_id": "jellyfin-user-xyz",
					},
				)

				Expect(w.Code).To(Equal(http.StatusConflict))
			})
		})

		Context("when required fields are missing", func() {
			It("returns 400 when user_id is missing", func() {
				w := doPost(router, "/proxy/backends/"+backend.ID.String()+"/users",
					map[string]interface{}{
						"backend_user_id": "jellyfin-user-abc",
					},
				)
				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})

			It("returns 400 when backend_user_id is missing", func() {
				w := doPost(router, "/proxy/backends/"+backend.ID.String()+"/users",
					map[string]interface{}{
						"user_id": user.ID.String(),
					},
				)
				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("when the backend does not exist", func() {
			It("returns 404", func() {
				w := doPost(router, "/proxy/backends/00000000-0000-0000-0000-000000000001/users",
					map[string]interface{}{
						"user_id":         user.ID.String(),
						"backend_user_id": "jf-user",
					},
				)
				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("when the user does not exist", func() {
			It("returns 404", func() {
				w := doPost(router, "/proxy/backends/"+backend.ID.String()+"/users",
					map[string]interface{}{
						"user_id":         "00000000-0000-0000-0000-000000000099",
						"backend_user_id": "jf-user",
					},
				)
				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})
	})

	// ── ListBackendUsers ─────────────────────────────────────────────────────────

	Describe("ListBackendUsers", func() {
		var backend *ent.Backend

		BeforeEach(func() {
			backend = createBackend("Primary", "http://media.example.com", "s1")
		})

		Context("with no mappings", func() {
			It("returns 200 with an empty array", func() {
				w := doGet(router, "/proxy/backends/"+backend.ID.String()+"/users")

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp []interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).To(BeEmpty())
			})
		})

		Context("with multiple mappings", func() {
			It("returns all mappings for that backend", func() {
				u1 := createUser("alpha", "password1!", false)
				u2 := createUser("beta", "password1!", false)
				createBackendUser(backend, u1, "jf-alpha")
				createBackendUser(backend, u2, "jf-beta")

				w := doGet(router, "/proxy/backends/"+backend.ID.String()+"/users")

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp []interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).To(HaveLen(2))
			})
		})

		Context("when the backend does not exist", func() {
			It("returns 404", func() {
				w := doGet(router, "/proxy/backends/00000000-0000-0000-0000-000000000001/users")
				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("with a malformed backend UUID", func() {
			It("returns 400", func() {
				w := doGet(router, "/proxy/backends/not-a-uuid/users")
				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})

	// ── UpdateBackendUser ────────────────────────────────────────────────────────

	Describe("UpdateBackendUser", func() {
		var (
			backend *ent.Backend
			mapping *ent.BackendUser
		)

		BeforeEach(func() {
			backend = createBackend("Primary", "http://media.example.com", "s1")
			user := createUser("alice", "password1!", false)
			mapping = createBackendUser(backend, user, "jf-alice-old")
		})

		Context("updating backend_user_id", func() {
			It("returns 200 with the new backend_user_id", func() {
				w := doPatch(router,
					fmt.Sprintf("/proxy/backends/%s/users/%s", backend.ID, mapping.ID),
					map[string]string{"backend_user_id": "jf-alice-new"},
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["backend_user_id"]).To(Equal("jf-alice-new"))
			})
		})

		Context("toggling enabled", func() {
			It("returns 200 with enabled=false", func() {
				w := doPatch(router,
					fmt.Sprintf("/proxy/backends/%s/users/%s", backend.ID, mapping.ID),
					map[string]interface{}{"enabled": false},
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["enabled"]).To(BeFalse())
			})
		})

		Context("when no fields are provided", func() {
			It("returns 400", func() {
				w := doPatch(router,
					fmt.Sprintf("/proxy/backends/%s/users/%s", backend.ID, mapping.ID),
					map[string]interface{}{},
				)

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("when the mapping does not exist", func() {
			It("returns 404", func() {
				w := doPatch(router,
					fmt.Sprintf("/proxy/backends/%s/users/%s", backend.ID, "00000000-0000-0000-0000-000000000001"),
					map[string]string{"backend_user_id": "new-id"},
				)
				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("with a malformed mapping UUID", func() {
			It("returns 400", func() {
				w := doPatch(router,
					fmt.Sprintf("/proxy/backends/%s/users/%s", backend.ID, "not-a-uuid"),
					map[string]string{"backend_user_id": "new-id"},
				)
				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("updating backend_token", func() {
			It("returns 200 with the updated mapping", func() {
				w := doPatch(router,
					fmt.Sprintf("/proxy/backends/%s/users/%s", backend.ID, mapping.ID),
					map[string]interface{}{"backend_token": "new-token-value"},
				)

				Expect(w.Code).To(Equal(http.StatusOK))
			})
		})
	})

	// ── DeleteBackendUser ────────────────────────────────────────────────────────

	Describe("DeleteBackendUser", func() {
		Context("when the mapping exists", func() {
			It("returns 204", func() {
				backend := createBackend("Primary", "http://media.example.com", "s1")
				user := createUser("alice", "password1!", false)
				mapping := createBackendUser(backend, user, "jf-alice")

				w := doDelete(router,
					fmt.Sprintf("/proxy/backends/%s/users/%s", backend.ID, mapping.ID),
				)

				Expect(w.Code).To(Equal(http.StatusNoContent))
			})
		})

		Context("when the mapping does not exist", func() {
			It("returns 404", func() {
				backend := createBackend("Primary", "http://media.example.com", "s1")

				w := doDelete(router,
					fmt.Sprintf("/proxy/backends/%s/users/%s", backend.ID, "00000000-0000-0000-0000-000000000001"),
				)

				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("with a malformed mapping UUID", func() {
			It("returns 400", func() {
				backend := createBackend("Primary", "http://media.example.com", "s1")

				w := doDelete(router,
					fmt.Sprintf("/proxy/backends/%s/users/%s", backend.ID, "not-a-uuid"),
				)

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})

	// ── LoginToBackend ───────────────────────────────────────────────────────────

	Describe("LoginToBackend", func() {
		var (
			user    *ent.User
			backend *ent.Backend
		)

		// jellyfinMockServer returns a test HTTP server that accepts
		// POST /users/authenticatebyname and responds with a canned auth payload.
		jellyfinMockServer := func(status int) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == http.StatusOK {
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"User":        map[string]string{"Id": "backend-user-id"},
						"AccessToken": "backend-access-token",
					})
				}
			}))
		}

		BeforeEach(func() {
			user = createUser("alice", "password1!", false)
		})

		Context("when the backend accepts the credentials (new mapping)", func() {
			It("returns 201 and creates the BackendUser mapping", func() {
				mock := jellyfinMockServer(http.StatusOK)
				defer mock.Close()
				backend = createBackend("Primary", mock.URL, "s1")

				w := doPost(router, "/proxy/backends/"+backend.ID.String()+"/login",
					map[string]interface{}{
						"proxy_user_id": user.ID.String(),
						"username":      "alice",
						"password":      "correctpass",
					},
				)

				Expect(w.Code).To(Equal(http.StatusCreated))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["backend_user_id"]).To(Equal("backend-user-id"))
			})
		})

		Context("when a mapping already exists (upsert path)", func() {
			It("returns 200 and updates the existing mapping", func() {
				mock := jellyfinMockServer(http.StatusOK)
				defer mock.Close()
				backend = createBackend("Primary", mock.URL, "s1")
				// Pre-create the mapping so the handler hits the update branch.
				createBackendUser(backend, user, "old-backend-user-id")

				w := doPost(router, "/proxy/backends/"+backend.ID.String()+"/login",
					map[string]interface{}{
						"proxy_user_id": user.ID.String(),
						"username":      "alice",
						"password":      "correctpass",
					},
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["backend_user_id"]).To(Equal("backend-user-id"))
			})
		})

		Context("when the backend rejects the credentials", func() {
			It("returns 502", func() {
				mock := jellyfinMockServer(http.StatusUnauthorized)
				defer mock.Close()
				backend = createBackend("Primary", mock.URL, "s1")

				w := doPost(router, "/proxy/backends/"+backend.ID.String()+"/login",
					map[string]interface{}{
						"proxy_user_id": user.ID.String(),
						"username":      "alice",
						"password":      "wrongpass",
					},
				)

				Expect(w.Code).To(Equal(http.StatusBadGateway))
			})
		})

		Context("when the backend is unreachable", func() {
			It("returns 502", func() {
				// Use a URL on a port that isn't listening.
				backend = createBackend("Primary", "http://127.0.0.1:1", "s1")

				w := doPost(router, "/proxy/backends/"+backend.ID.String()+"/login",
					map[string]interface{}{
						"proxy_user_id": user.ID.String(),
						"username":      "alice",
						"password":      "anypass",
					},
				)

				Expect(w.Code).To(Equal(http.StatusBadGateway))
			})
		})

		Context("when the backend does not exist", func() {
			It("returns 404", func() {
				w := doPost(router, "/proxy/backends/00000000-0000-0000-0000-000000000001/login",
					map[string]interface{}{
						"proxy_user_id": user.ID.String(),
						"username":      "alice",
						"password":      "anypass",
					},
				)

				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("when required fields are missing", func() {
			It("returns 400", func() {
				mock := jellyfinMockServer(http.StatusOK)
				defer mock.Close()
				backend = createBackend("Primary", mock.URL, "s1")

				w := doPost(router, "/proxy/backends/"+backend.ID.String()+"/login",
					map[string]interface{}{
						"username": "alice",
					},
				)
				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("with a malformed backend UUID", func() {
			It("returns 400", func() {
				w := doPost(router, "/proxy/backends/not-a-uuid/login",
					map[string]interface{}{
						"proxy_user_id": user.ID.String(),
						"username":      "alice",
						"password":      "anypass",
					},
				)
				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})
})
