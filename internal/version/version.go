package version

// Version is injected by release builds. Development builds keep "dev".
var Version = "dev"

// Release artifacts retain their source identity for deployment verification.
var Revision string
var SourceTree string
var UpstreamRevision string

func Current() string {
	return Version
}
