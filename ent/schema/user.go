package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// User holds proxy-managed user accounts.
type User struct {
	ent.Schema
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New),
		field.String("username").
			Unique().
			NotEmpty(),
		field.String("display_name").
			NotEmpty(),
		field.String("hashed_password").
			Sensitive().
			NotEmpty(),
		field.Bool("is_admin").
			Default(false),
		field.Bool("direct_stream").
			Default(false),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
		field.Bytes("avatar").
			Optional().
			Nillable(),
		field.String("avatar_content_type").
			Optional().
			Nillable(),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("sessions", Session.Type),
		edge.To("backend_users", BackendUser.Type),
	}
}
