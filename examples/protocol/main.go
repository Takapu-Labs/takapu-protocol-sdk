// protocol demonstrates quote signing, contract calldata and decay offline,
// with optional RPC reads and swap simulation when -config is supplied.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "", "JSON configuration for RPC reads and swap simulation; empty runs offline")
	timeout := flag.Duration("timeout", 30*time.Second, "Timeout for the RPC workflow")
	flag.Parse()

	err := func() error {
		if flag.NArg() != 0 {
			return errors.New("unexpected positional arguments; use -h for usage")
		}
		if *timeout <= 0 {
			return errors.New("timeout must be positive")
		}
		if *configPath == "" {
			return runOffline()
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		return runRPC(ctx, *configPath, os.Stdout)
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
