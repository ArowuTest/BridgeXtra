// Package buildinfo carries the build identity a running process reports at /version
// (BX-MED-005). Knowing exactly which commit is serving traffic is an operational
// prerequisite for incident response: "which build is live?" must be answerable from the
// process itself, not inferred from a deploy dashboard.
//
// Commit/Version are stamped at link time:
//
//	go build -ldflags "-X github.com/ArowuTest/telco-credit-platform/backend/internal/platform/buildinfo.Commit=$GITHUB_SHA"
//
// When they are not stamped (a plain `go build`, `go run`, or `go test`), Info falls back to
// the VCS metadata the Go toolchain embeds automatically, so the endpoint is truthful in
// development instead of confidently wrong.
package buildinfo

import "runtime/debug"

// Stamped at link time. Deliberately plain vars: -X can only set string variables.
var (
	Version = ""
	Commit  = ""
)

// Details is what /version reports. It carries build identity ONLY — never configuration,
// environment, DSNs or secrets: the endpoint is reachable wherever its plane is reachable.
type Details struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
	Modified  bool   `json:"modified"` // built from a dirty working tree
}

// Info resolves build identity, preferring link-time stamps and falling back to the
// toolchain's embedded VCS data.
func Info() Details {
	d := Details{Version: Version, Commit: Commit}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		if d.Version == "" {
			d.Version = "unknown"
		}
		if d.Commit == "" {
			d.Commit = "unknown"
		}
		return d
	}
	d.GoVersion = bi.GoVersion
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if d.Commit == "" {
				d.Commit = s.Value
			}
		case "vcs.modified":
			d.Modified = s.Value == "true"
		}
	}
	if d.Version == "" {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			d.Version = bi.Main.Version
		} else {
			d.Version = "devel"
		}
	}
	if d.Commit == "" {
		d.Commit = "unknown"
	}
	return d
}
