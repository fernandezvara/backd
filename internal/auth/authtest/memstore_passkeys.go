package authtest

import (
	"context"

	"github.com/fernandezvara/backd/internal/auth/passkey"
)

// The passkey.Store methods, kept by the in-memory passkey store.

func (m *MemStore) Add(ctx context.Context, c passkey.Credential) error {
	return m.passkeys.Add(ctx, c)
}

func (m *MemStore) ByCredentialID(ctx context.Context, id []byte) (passkey.Credential, error) {
	return m.passkeys.ByCredentialID(ctx, id)
}

func (m *MemStore) ListByUser(ctx context.Context, userID string) ([]passkey.Credential, error) {
	return m.passkeys.ListByUser(ctx, userID)
}

func (m *MemStore) Update(ctx context.Context, credentialID []byte, u passkey.Update) error {
	return m.passkeys.Update(ctx, credentialID, u)
}

func (m *MemStore) Delete(ctx context.Context, userID, id string) error {
	return m.passkeys.Delete(ctx, userID, id)
}

func (m *MemStore) CountByUser(ctx context.Context, userID string) (int, error) {
	return m.passkeys.CountByUser(ctx, userID)
}

func (m *MemStore) UserHandle(ctx context.Context, userID string) ([]byte, error) {
	return m.passkeys.UserHandle(ctx, userID)
}

func (m *MemStore) UserByHandle(ctx context.Context, handle []byte) (string, error) {
	return m.passkeys.UserByHandle(ctx, handle)
}
