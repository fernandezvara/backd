package auth

import "github.com/fernandezvara/backd/internal/authcore"

// The store-free parts of authentication live in authcore, so the CLI can use
// them without the stores; these names keep them part of auth's API.

type (
	Scope        = authcore.Scope
	ScopeOp      = authcore.ScopeOp
	Scopes       = authcore.Scopes
	SecretCipher = authcore.SecretCipher
)

const (
	ScopeRead  = authcore.ScopeRead
	ScopeWrite = authcore.ScopeWrite
	ScopeCall  = authcore.ScopeCall
	MaxScopes  = authcore.MaxScopes
	KeyLength  = authcore.KeyLength
	PlainKey   = authcore.PlainKey
)

var (
	ErrKeyValue         = authcore.ErrKeyValue
	PortableCollections = authcore.PortableCollections
	PortableSealed      = authcore.PortableSealed
)

var (
	ParseScope             = authcore.ParseScope
	ParseScopes            = authcore.ParseScopes
	NewSecretCipher        = authcore.NewSecretCipher
	SealWith               = authcore.SealWith
	OpenWith               = authcore.OpenWith
	KeyFingerprint         = authcore.KeyFingerprint
	DecodeKey              = authcore.DecodeKey
	NewKey                 = authcore.NewKey
	PortableReset          = authcore.PortableReset
	PortableToPlain        = authcore.PortableToPlain
	PortableWithoutSecrets = authcore.PortableWithoutSecrets
	ExportKey              = authcore.ExportKey
)
