package main

import "context"

// canceledContext aliases context.Context so test files can construct one in
// a single place.
type canceledContext = context.Context

func canceledContextFromBackground() (canceledContext, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
