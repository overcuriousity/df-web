package main

import "context"

type contextKey string

const uidKey contextKey = "uid"

func withUID(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, uidKey, uid)
}

func uidFromContext(ctx context.Context) string {
	uid, _ := ctx.Value(uidKey).(string)
	return uid
}
