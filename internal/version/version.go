// Package version carries the rocc build version, stamped by the release
// workflow via -ldflags "-X rocc/internal/version.Version=v1.2.3"; local
// builds say "dev".
package version

var Version = "dev"
