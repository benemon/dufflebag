package identity

import "context"

type actorContextKey struct{}

type Actor struct {
	PrincipalID string
	Name        string
}

func WithActor(ctx context.Context, principalID, name string) context.Context {
	return context.WithValue(ctx, actorContextKey{}, Actor{PrincipalID: principalID, Name: name})
}

func ActorFromContext(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(actorContextKey{}).(Actor)
	return actor, ok
}
