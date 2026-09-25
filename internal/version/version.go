package version

// Version is injected by release builds. Development builds keep "dev".
var Version = "dev"

// Build provenance is injected by the verified source-image release pipeline.
var Revision string
var SourceTree string
var UpstreamRevision string

func Current() string {
	return Version
}
