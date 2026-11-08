// Package version holds build information stamped in with -ldflags -X by the
// Dockerfile. A plain `go run` reports "dev".
package version

var (
	Version   = "dev"
	Commit    = ""
	Branch    = ""
	BuildTime = ""
)

// Info is what GET /api/v1/version returns. The updater compares Commit with
// the commit it just deployed to decide whether the new container is healthy.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Branch    string `json:"branch"`
	BuildTime string `json:"build_time"`
}

func Get() Info {
	return Info{Version: Version, Commit: Commit, Branch: Branch, BuildTime: BuildTime}
}
