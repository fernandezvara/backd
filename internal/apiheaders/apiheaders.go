// Package apiheaders names the HTTP headers that both the server and the CLI
// read, so the CLI doesn't need the server's packages for them.
package apiheaders

// DevDuration and DevLogs carry the function's timing and console output back
// to the caller, only in dev mode (BACKD_DEV): a production call never leaks
// another caller's function logs. `backd functions invoke` reads them.
const (
	DevDuration = "X-Backd-Dev-Duration-Ms"
	DevLogs     = "X-Backd-Dev-Logs"
)
