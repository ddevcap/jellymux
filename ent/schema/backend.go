package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// Backend represents a backend Jellyfin server the proxy federates.
type Backend struct {
	ent.Schema
}

func (Backend) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New),
		field.String("name").
			NotEmpty(),
		field.String("url").
			NotEmpty().
			Comment("Base URL of the backend Jellyfin server, e.g. https://media.example.com"),
		// The server ID reported by Jellyfin's /System/Info endpoint.
		field.String("jellyfin_server_id").
			Unique().
			NotEmpty(),
		field.Bool("enabled").
			Default(true),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

func (Backend) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("backend_users", BackendUser.Type),
	}
}
