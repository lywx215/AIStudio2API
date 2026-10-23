package contract

import "context"

type consoleKey struct{}

// Console lifetime is supplied only by the existing authenticated console bridge.
// It survives an individual polling request, but never logout/session expiry.
type Console struct {
	ID       string
	Lifetime context.Context
}

func WithConsole(ctx context.Context, id string, lifetime context.Context) context.Context {
	return context.WithValue(ctx, consoleKey{}, Console{id, lifetime})
}
func ConsoleFrom(ctx context.Context) (Console, bool) {
	c, ok := ctx.Value(consoleKey{}).(Console)
	return c, ok && c.ID != "" && c.Lifetime != nil && c.Lifetime.Err() == nil
}
