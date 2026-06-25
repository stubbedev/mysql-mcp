// Command mysql-mcp is an MCP server exposing MySQL/MariaDB databases to MCP
// clients over stdio or HTTP.
package main

import (
	"fmt"
	"os"

	"github.com/stubbedev/mysql-mcp/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
