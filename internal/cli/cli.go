// Package cli implements the mysql-mcp command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/stubbedev/mysql-mcp/internal/config"
	"github.com/stubbedev/mysql-mcp/internal/mcpserver"
	"github.com/stubbedev/mysql-mcp/internal/source"
)

// Build information, overridable via -ldflags at build time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Execute runs the root command.
func Execute() error {
	return rootCmd().Execute()
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "mysql-mcp",
		Short:         "MCP server for MySQL/MariaDB databases",
		Long:          "mysql-mcp is a Model Context Protocol server that exposes MySQL/MariaDB databases (local or SSH-tunneled) to MCP clients over stdio or HTTP.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(serveCmd(), genSchemaCmd(), genConfigDocsCmd(), versionCmd())
	return root
}

func serveCmd() *cobra.Command {
	var (
		configPath string
		transport  string
		httpAddr   string
		readOnly   bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Logs go to stderr so they never corrupt the stdio JSON-RPC stream.
			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

			// The global config is optional: without one the server runs in
			// roots-only mode, serving each client from its workspace's
			// RootConfigName file. An explicit --config that fails to load is
			// still fatal.
			var (
				base        *source.Registry
				baseTimeout time.Duration
				httpCfg     = config.DefaultHTTPConfig()
			)
			cfg, err := config.Load(configPath)
			switch {
			case err == nil:
				base, err = source.NewRegistry(cfg)
				if err != nil {
					return err
				}
				defer base.Close()
				baseTimeout = time.Duration(cfg.QueryTimeoutSeconds) * time.Second
				httpCfg = cfg.HTTP
			case configPath == "" && errors.Is(err, config.ErrNotFound):
				logger.Warn("no global config; serving in roots-only mode",
					"hint", "each client must expose a workspace root containing "+config.RootConfigName)
			default:
				return err
			}
			if httpAddr != "" {
				httpCfg.Addr = httpAddr
			}

			srv := mcpserver.New(base, baseTimeout, Version, readOnly)

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			switch transport {
			case "stdio":
				logger.Info("serving MCP over stdio", "sources", registryNames(base))
				return mcpserver.ServeStdio(ctx, srv)
			case "http":
				return mcpserver.ServeHTTP(ctx, srv, httpCfg, logger)
			default:
				return fmt.Errorf("unknown transport %q (want stdio or http)", transport)
			}
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "path to global config file (default: XDG config dir; optional when clients supply per-workspace .mysql-mcp.json via MCP roots)")
	cmd.Flags().StringVarP(&transport, "transport", "t", "stdio", "transport: stdio or http")
	cmd.Flags().StringVar(&httpAddr, "http-addr", "", "HTTP listen address (overrides config http.addr)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "force every source read-only regardless of config")
	return cmd
}

func genSchemaCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "gen-schema",
		Short: "Generate the JSON Schema for the config file",
		RunE: func(_ *cobra.Command, _ []string) error {
			data, err := config.GenerateSchema()
			if err != nil {
				return err
			}
			return writeOutput(output, data)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "schema/config.schema.json", "output path, or - for stdout")
	return cmd
}

func genConfigDocsCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "gen-config-docs",
		Short: "Generate Markdown docs for the config file",
		RunE: func(_ *cobra.Command, _ []string) error {
			md, err := config.GenerateDocs()
			if err != nil {
				return err
			}
			return writeOutput(output, []byte(md))
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "docs/configuration.md", "output path, or - for stdout")
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "mysql-mcp %s (commit %s, built %s)\n", Version, Commit, Date)
			return err
		},
	}
}

// registryNames returns the fallback registry's source names for a startup log
// line; nil registry (roots-only mode) yields none.
func registryNames(reg *source.Registry) []string {
	if reg == nil {
		return nil
	}
	return reg.Names()
}

// writeOutput writes data to path, or to stdout when path is "-".
func writeOutput(path string, data []byte) error {
	if path == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	return nil
}
