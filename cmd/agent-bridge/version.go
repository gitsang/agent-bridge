package main

import (
	"fmt"
	"log/slog"
	"runtime"

	"github.com/rodaine/table"
	"github.com/spf13/cobra"
)

var (
	Version   = "dev"
	BuildDate = "1970-01-01T00:00:00Z"
	GoVersion = ""
	GOOS      = ""
	GOARCH    = ""
	GitCommit = ""
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Display the version information",
	Run: func(cmd *cobra.Command, args []string) {
		{
			tbl := table.New("Build", "")
			tbl.AddRow("Version", Version)
			tbl.AddRow("BuildDate", BuildDate)
			tbl.AddRow("Go Version", GoVersion)
			tbl.AddRow("OS/Arch", GOOS+"/"+GOARCH)
			tbl.AddRow("Git Commit", GitCommit)
			tbl.Print()
		}
		fmt.Println()
		{
			tbl := table.New("Runtime", "")
			tbl.AddRow("Go Version", runtime.Version())
			tbl.AddRow("OS/Arch", runtime.GOOS+"/"+runtime.GOARCH)
			tbl.Print()
		}
	},
}

var versionLog = slog.Group("version",
	slog.Group("build",
		slog.String("version", Version),
		slog.String("buildDate", BuildDate),
		slog.String("go version", GoVersion),
		slog.String("os/arch", GOOS+"/"+GOARCH),
	),
	slog.Group("runtime",
		slog.String("go version", runtime.Version()),
		slog.String("os/arch", runtime.GOOS+"/"+runtime.GOARCH),
	),
)

func init() {
	rootCmd.AddCommand(versionCmd)
}
