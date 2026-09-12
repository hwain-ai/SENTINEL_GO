package main

import (
	"os"

	"github.com/hwain-hwang/sentinel-go/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
