// Package main is the qlvm-agent D-Bus daemon entry point.
// It runs as root under systemd, exposes io.qlvm.Agent on the system bus,
// and delegates to qlvm's internal packages.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jcpowermac/qlvm/internal/agent"
	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/coremap"
	"github.com/jcpowermac/qlvm/internal/ovn"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

// configPath is where the daemon reads its configuration.
const configPath = "/etc/qvm/qlvm.yaml"

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		cancel()
	}()

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}

	x, err := xenctl.New()
	if err != nil {
		log.Fatalf("xen: %v", err)
	}
	defer func() { _ = x.Close() }()

	nb, err := ovn.NewLive(ctx)
	if err != nil {
		log.Fatalf("ovn: %v", err)
	}
	defer nb.Close()
	br, err := ovs.NewLive(ctx)
	if err != nil {
		log.Fatalf("ovs: %v", err)
	}
	defer br.Close()

	a := agent.New(x, nb, br, cfg, coremap.DefaultPath)
	name, err := a.Run(ctx)
	if err != nil {
		log.Fatalf("agent: %v", err)
	}
	log.Printf("qlvm-agent running on system bus as %s", name)
}
