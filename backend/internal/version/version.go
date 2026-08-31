// Package version exposes build metadata that is injected at link time.
package version

import (
	"fmt"
	"runtime"
)

// These values are overridden with -ldflags at build time, for example:
//
//	go build -ldflags "-X github.com/joshuanmoses/piCloud/backend/internal/version.Version=1.0.0"
var (
	// Version is the semantic version of this build.
	Version = "1.0.0-dev"
	// Commit is the git revision this binary was built from.
	Commit = "unknown"
	// BuildDate is the RFC 3339 timestamp of the build.
	BuildDate = "unknown"
)

// Info describes the running build. It is safe to expose publicly: it contains
// no secrets and no infrastructure detail.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	Go        string `json:"go"`
	Platform  string `json:"platform"`
}

// Current returns the build information for this binary.
func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		Go:        runtime.Version(),
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}

// String renders a single-line human-readable summary.
func (i Info) String() string {
	return fmt.Sprintf("PiFiles %s (commit %s, built %s, %s, %s)",
		i.Version, i.Commit, i.BuildDate, i.Go, i.Platform)
}
