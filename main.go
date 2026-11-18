// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Command wake operates the Wake daemon.
package main

import (
	"os"

	"go.trippwill.dev/wake/internal/transport/cli"
)

func main() { os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr)) }
