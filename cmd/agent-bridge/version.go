package main

import (
	"fmt"
	"log/slog"
	"runtime"

	"github.com/gitsang/agent-bridge/internal/version"
	"github.com/rodaine/table"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Display the version information",
	Run: func(cmd *cobra.Command, args []string) {
		{
			tbl := table.New("Build", "")
			tbl.AddRow("Version", version.Version)
			tbl.AddRow("BuildDate", version.BuildDate)
			tbl.AddRow("Go Version", version.GoVersion)
			tbl.AddRow("OS/Arch", version.GOOS+"/"+version.GOARCH)
			tbl.AddRow("Git Commit", version.GitCommit)
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
		slog.String("version", version.Version),
		slog.String("buildDate", version.BuildDate),
		slog.String("go version", version.GoVersion),
		slog.String("os/arch", version.GOOS+"/"+version.GOARCH),
	),
	slog.Group("runtime",
		slog.String("go version", runtime.Version()),
		slog.String("os/arch", runtime.GOOS+"/"+runtime.GOARCH),
	),
)

func init() {
	rootCmd.AddCommand(versionCmd)
}
