package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mishankov/llm-meter/internal/relay"
)

type options struct {
	relay.Config
	keyFile string
}

func main() {
	var opts options
	flag.StringVar(&opts.Listen, "listen", relay.DefaultListen, "client-facing loopback IP and port")
	flag.StringVar(&opts.Bind, "bind", "", "container socket address, e.g. 0.0.0.0:8765 with a loopback-only Docker port mapping")
	flag.StringVar(&opts.Mode, "mode", relay.ModeRemoteProxy, "remote-proxy or usage-only")
	flag.StringVar(&opts.Provider, "provider", "", "restrict to antigravity, openai, codex, or opencode-go (default all)")
	flag.StringVar(&opts.Target, "target", "", "HTTPS LLM Meter origin")
	flag.StringVar(&opts.Upstream, "upstream", "", "HTTPS provider base URL override in usage-only mode with --provider")
	flag.StringVar(&opts.keyFile, "key-file", "", "path to the private proxy key file")
	flag.StringVar(&opts.DataDir, "data-dir", "./data/relay", "persistent directory for this connection's pending usage in usage-only mode")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("unexpected positional arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, opts); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, opts options) error {
	key, err := relay.ReadKeyFile(opts.keyFile)
	if err != nil {
		return err
	}
	opts.Key = key
	inst, err := relay.New(opts.Config, relay.WithLogObserver(func(entry relay.LogEntry) {
		log.Print(entry.Message)
	}))
	if err != nil {
		return err
	}
	if err := inst.Start(); err != nil {
		return err
	}
	select {
	case <-inst.Done():
		return inst.Err()
	case <-ctx.Done():
		stopCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		return inst.Stop(stopCtx)
	}
}
