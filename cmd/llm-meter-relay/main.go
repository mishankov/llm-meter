package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mishankov/llm-meter/internal/relay"
)

type options struct {
	relay.Config
	keyFile string
}

func main() {
	opts, err := parseFlags(flag.CommandLine, os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, opts); err != nil {
		log.Fatal(err)
	}
}

func parseFlags(flags *flag.FlagSet, args []string) (options, error) {
	var opts options
	proxy := flags.Bool("proxy", true, "enable the model proxy in remote-proxy or usage-only mode")
	flags.StringVar(&opts.Listen, "listen", relay.DefaultListen, "client-facing loopback IP and port for the primary listener")
	flags.StringVar(&opts.Bind, "bind", "", "container socket for the primary listener, e.g. 0.0.0.0:8765 with a loopback-only Docker port mapping")
	flags.StringVar(&opts.Mode, "mode", relay.ModeRemoteProxy, "remote-proxy, usage-only, or telemetry for a standalone OTLP receiver")
	flags.StringVar(&opts.Provider, "provider", "", "restrict to antigravity or openai (default both)")
	flags.StringVar(&opts.Target, "target", "", "HTTPS LLM Meter origin")
	flags.StringVar(&opts.Upstream, "upstream", "", "HTTPS provider base URL override in usage-only mode with --provider")
	flags.StringVar(&opts.keyFile, "key-file", "", "path to the private proxy key file")
	flags.StringVar(&opts.DataDir, "data-dir", "./data/relay", "persistent directory for pending local usage reports")
	flags.StringVar(&opts.TelemetryListen, "telemetry-listen", "", "enable OTLP/HTTP at this loopback IP and port, e.g. 127.0.0.1:4318")
	flags.StringVar(&opts.TelemetryBind, "telemetry-bind", "", "container socket for --telemetry-listen, e.g. 0.0.0.0:4318 with a loopback-only Docker port mapping")
	flags.StringVar(&opts.TLSCert, "tls-cert", "", "PEM server certificate for HTTPS with --mode=telemetry or --telemetry-listen")
	flags.StringVar(&opts.TLSKey, "tls-key", "", "PEM private key for HTTPS with --mode=telemetry or --telemetry-listen")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("unexpected positional arguments")
	}
	if *proxy || opts.Mode == relay.ModeTelemetry {
		return opts, nil
	}

	if opts.Mode != relay.ModeRemoteProxy && opts.Mode != relay.ModeUsageOnly {
		return options{}, errors.New("--mode must be remote-proxy, usage-only, or telemetry")
	}
	if strings.TrimSpace(opts.TelemetryListen) == "" {
		return options{}, errors.New("no listeners enabled: set --telemetry-listen or enable the proxy")
	}
	opts.Mode = relay.ModeTelemetry
	opts.Listen, opts.Bind = opts.TelemetryListen, opts.TelemetryBind
	opts.TelemetryListen, opts.TelemetryBind = "", ""
	opts.Provider, opts.Upstream = "", ""
	return opts, nil
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
