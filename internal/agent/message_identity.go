package agent

import "context"

type messageIdentityContextKey struct{}

// Tool work captures the committed message identity in its context. It never
// reads a mutable "latest message" from the Agent while tools run concurrently.
func withMessageIdentity(ctx context.Context, messageID string) context.Context {
	return context.WithValue(ctx, messageIdentityContextKey{}, messageID)
}

func messageIdentity(ctx context.Context) string {
	id, _ := ctx.Value(messageIdentityContextKey{}).(string)
	return id
}

type userMessageIdentityKey struct{}
type userMessageIdentity struct {
	session *Session
	id      string
}

func withUserMessageIdentity(ctx context.Context, session *Session, id string) context.Context {
	return context.WithValue(ctx, userMessageIdentityKey{}, userMessageIdentity{session: session, id: id})
}

// WithUserMessageIdentity reserves the controller's user identity before a
// planner can emit display events. Cancellation recovery uses the same ID.
func WithUserMessageIdentity(ctx context.Context, session *Session, id string) context.Context {
	return withUserMessageIdentity(ctx, session, id)
}

func turnUserMessageID(ctx context.Context, session *Session) string {
	if identity, ok := ctx.Value(userMessageIdentityKey{}).(userMessageIdentity); ok && identity.session == session {
		return identity.id
	}
	return NewMessageID()
}
