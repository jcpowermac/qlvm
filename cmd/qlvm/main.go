// Package main is the qlvm CLI entry point.
package main

import (
	"os"

	"github.com/jcpowermac/qlvm/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
