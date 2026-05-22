package discovery

import (
	"net/http"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// NewGraphQLHandler builds the gqlgen HTTP handler for the discovery API.
func NewGraphQLHandler(pool *pgxpool.Pool, rdb *redis.Client) http.Handler {
	return handler.NewDefaultServer(NewExecutableSchema(Config{
		Resolvers: &Resolver{Pool: pool, Redis: rdb},
	}))
}

// Playground serves the interactive GraphQL playground UI.
func Playground() http.Handler {
	return playground.Handler("Event Discovery", "/graphql")
}
