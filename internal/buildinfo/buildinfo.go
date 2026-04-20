package buildinfo

import "runtime"

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
	BuiltBy   = "unknown"
)

type Info struct {
	Version   string
	Commit    string
	BuildTime string
	BuiltBy   string
	GoVersion string
	Platform  string
}

func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		BuiltBy:   BuiltBy,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

func (i Info) Lines() []string {
	return []string{
		"version=" + i.Version,
		"commit=" + i.Commit,
		"build_time=" + i.BuildTime,
		"built_by=" + i.BuiltBy,
		"go_version=" + i.GoVersion,
		"platform=" + i.Platform,
	}
}
