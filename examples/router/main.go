// router receives frames through the Router SDK and caches them locally.
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
	configPath := flag.String("config", "config.local.json", "Router JSON configuration file")
	includeGray := flag.Bool("include-test-gray", false, "Legacy option: append the test_gray subscription; use filters in new configurations")
	poll := flag.Duration("poll", 200*time.Millisecond, "Interval for displaying frames from the application's cache")
	statusEvery := flag.Duration("status-every", 10*time.Second, "Interval for logging connection and cache statistics")
	duration := flag.Duration("duration", 0, "Subscription duration; 0 runs until Ctrl+C")
	maxCache := flag.Int("max-cached-quotes", 10000, "Maximum number of chain/pair/maker streams in the application's cache")
	frameBuffer := flag.Int("frame-buffer", 256, "SDK frame channel capacity; overflow stops the connection")
	flag.Parse()

	var cfg config
	err := func() error {
		if *poll <= 0 || *statusEvery <= 0 || *duration < 0 || *maxCache <= 0 || *frameBuffer <= 0 {
			return errors.New("poll, status-every, max-cached-quotes, and frame-buffer must be positive; duration must not be negative")
		}
		file, err := os.Open(*configPath)
		if err != nil {
			return err
		}
		cfg, err = readConfig(file)
		err = errors.Join(err, file.Close())
		if err != nil {
			return err
		}
		if err := cfg.validate(*includeGray); err != nil {
			return err
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if *duration > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, *duration)
			defer cancel()
		}
		cfg.logf("info", "Receiving every frame into an application-owned cache; displaying the latest frames every %s", *poll)
		cfg.logf("run", "Press Ctrl+C to stop; duration=%s (0 means no time limit)", *duration)
		return run(ctx, cfg, runOptions{
			includeTestGray: *includeGray,
			poll:            *poll,
			statusEvery:     *statusEvery,
			maxCache:        *maxCache,
			frameBuffer:     *frameBuffer,
		})
	}()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s [error] %s\n", time.Now().Format("15:04:05.000"), cfg.redact(err.Error()))
		os.Exit(1)
	}
}
