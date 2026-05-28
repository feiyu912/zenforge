package main

import (
	"context"
	"os"

	"github.com/feiyu912/zenforge/cli"
)

func main() {
	os.Exit(cli.Main(context.Background(), os.Args[1:], cli.IO{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}))
}
