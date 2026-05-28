package version

import (
	"fmt"
	"runtime"
)

var (
	Version   = "dev"
	BuildDate = "1970-01-01T00:00:00Z"
	GoVersion = ""
	GOOS      = ""
	GOARCH    = ""
	GitCommit = ""
)

func String() string {
	if Version == "dev" {
		return fmt.Sprintf("dev (%s)", runtime.Version())
	}
	return Version
}
