// Package authcore holds the parts of backd's authentication that need no
// store: API key scopes, the secrets cipher, field key encoding, and the
// portable form of authentication data in export packages. It never imports
// the MongoDB driver, so the CLI uses it without linking the server.
// internal/auth builds the stores on it.
package authcore
