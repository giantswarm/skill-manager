// Package cmd holds the skill-manager CLI.
package cmd

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/giantswarm/skill-manager/internal/buildinfo"
)

// build is the running binary's identity: the release version, the commit
// and the build time, resolved by main from the ldflags values and the Go
// build info.
var build = buildinfo.Info{Version: buildinfo.DevVersion, Commit: buildinfo.UnknownCommit, Date: buildinfo.UnknownDate}

// SetBuild records the build identity the commands report.
func SetBuild(b buildinfo.Info) { build = b }

func newRootCmd() *cobra.Command {
	var verbose bool
	root := &cobra.Command{
		Use:   "skill-manager",
		Short: "Skill service for the Agent Platform",
		Long: `skill-manager owns the Agent Platform's skill sources: it lists the skills
they hold and pins skills and plugins to commits and digests, for the person
asking. It is one MCP server behind muster and a REST API for agent-manager
and the portal.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			level := slog.LevelInfo
			if verbose {
				level = slog.LevelDebug
			}
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
		},
	}
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable debug logging")
	root.Version = build.Version
	root.SetVersionTemplate("skill-manager version {{.Version}}\n")
	root.AddCommand(newServeCmd(), newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "skill-manager version %s\n  commit: %s\n  built:  %s\n", build.Version, build.Commit, build.Date)
		},
	}
}

// Execute runs the CLI.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
