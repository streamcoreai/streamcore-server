package signaling

import (
	"context"
	"net/http"
)

// Set by trusted server-side callers only; sip-server puts the calling number
// here. Not in the CORS allow-list, so browsers can't send it.
const ResourceIDHeader = "X-StreamCore-Resource-Id"

type resourceIDKey struct{}

// WithResourceID attaches an identity that has already been verified. Today
// that means the JWT middleware, after checking the signature.
func WithResourceID(ctx context.Context, resourceID string) context.Context {
	if resourceID == "" {
		return ctx
	}
	return context.WithValue(ctx, resourceIDKey{}, resourceID)
}

func ResourceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(resourceIDKey{}).(string)
	return id
}

// A verified claim beats the header: anyone can set a header, nobody can forge
// a signature. Empty when the deployment has no identity to offer.
func resolveResourceID(r *http.Request) string {
	if claimed := ResourceIDFromContext(r.Context()); claimed != "" {
		return claimed
	}
	return r.Header.Get(ResourceIDHeader)
}
