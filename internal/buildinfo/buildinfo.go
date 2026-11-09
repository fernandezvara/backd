// Package buildinfo is the version backd was built as, for every binary.
package buildinfo

import "runtime/debug"

// Version is set at build time (-ldflags "-X
// github.com/fernandezvara/backd/internal/buildinfo.Version=v1.2.3") by
// releases and the Dockerfiles; "dev" for local builds.
var Version = "dev"

// Commit is the VCS revision the binary was built from ("unknown" when the
// build has none, such as a container build without .git).
func Commit() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return s.Value[:min(len(s.Value), 12)]
			}
		}
	}
	return "unknown"
}
