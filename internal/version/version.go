// Package version holds the build version, set at link time:
//
//	go build -ldflags "-X github.com/khangphantz/aatmt/internal/version.Version=v1.0.0"
package version

// Version is "dev" for local builds and the release tag for release builds.
var Version = "dev"
