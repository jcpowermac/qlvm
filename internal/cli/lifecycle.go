package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/ovn"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/template"
	"github.com/jcpowermac/qlvm/internal/vm"
	"github.com/jcpowermac/qlvm/internal/xenctl"
)

// vmDirOf is the state dir of a VM under the qlvm root (spec §5.2).
func vmDirOf(name string) string {
	return filepath.Join(installRoot, "vms", name)
}

// xenctlNew creates a live Xen connection. Replaced in tests.
var xenctlNew = xenctl.New

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Boot a prepared VM (cleans stale OVS vif ports first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := startVM(cmd.Context(), args[0]); err != nil {
				return fmt.Errorf("start %s: %w", args[0], err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "started %s\n", args[0])
			return nil
		},
	}
}

// startVM boots a prepared VM (the qlvm vm start body); shared with apps'
// auto-start of a stopped VM.
func startVM(ctx context.Context, name string) error {
	dir := vmDirOf(name)
	m, err := vm.LoadMeta(dir)
	if err != nil {
		return err
	}
	tpl, err := template.LoadByRef(installRoot, m.Image, m.Digest)
	if err != nil {
		return fmt.Errorf("template: %w", err)
	}
	x, err := xenctl.New()
	if err != nil {
		return err
	}
	defer func() { _ = x.Close() }()
	br, err := ovs.NewLive(ctx)
	if err != nil {
		return err
	}
	return xenctl.Start(ctx, x, br, m, dir, tpl)
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Gracefully stop a running VM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			x, err := xenctlNew()
			if err != nil {
				return err
			}
			defer func() { _ = x.Close() }()
			if err := stopForced(x, args[0]); err != nil {
				return fmt.Errorf("stop %s: %w", args[0], err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "stopped %s\n", args[0])
			return nil
		},
	}
}

func killCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kill <name>",
		Short: "Force-kill a running VM (no clean shutdown)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			x, err := xenctl.New()
			if err != nil {
				return err
			}
			defer func() { _ = x.Close() }()
			if err := x.Destroy(args[0]); err != nil {
				return fmt.Errorf("kill %s: %w", args[0], err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "killed %s\n", args[0])
			return nil
		},
	}
}

// stopPollInterval/stopMaxWait govern stopForced's post-shutdown poll loop.
// Package vars so tests can shorten them; a healthy bootc guest powers off
// in well under the 60 s default, the zombie path costs 60 s before the
// force-kill.
var (
	stopPollInterval = 1500 * time.Millisecond
	stopMaxWait      = 60 * time.Second
)

// stopForced stops a domain gracefully and force-kills if it refuses ACPI
// or lingers after a successful shutdown (zombie domains never answer
// ACPI, and Shutdown only queues the event — it never waits).
func stopForced(x xenctl.Xen, name string) error {
	if err := x.Shutdown(name); err != nil {
		if derr := x.Destroy(name); derr != nil {
			return fmt.Errorf("stop %s: %w; destroy fallback: %w", name, err, derr)
		}
		return nil
	}
	// Shutdown is fire-and-forget: the ACPI event is queued and libxl
	// returns immediately, so success proves nothing. Poll until the
	// domain is actually gone; a domain that lingers (the in-guest-reboot
	// zombie, ---sr-, never answers ACPI) gets force-killed — Destroy is
	// the verified recovery.
	deadline := time.Now().Add(stopMaxWait)
	for {
		running, err := x.Running(name)
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		if !time.Now().Add(stopPollInterval).Before(deadline) {
			break
		}
		time.Sleep(stopPollInterval)
	}
	return x.Destroy(name)
}

func restartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <name>",
		Short: "Restart a VM (graceful stop, force-kill if it lingers, then start)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			x, err := xenctl.New()
			if err != nil {
				return err
			}
			defer func() { _ = x.Close() }()
			if err := stopForced(x, name); err != nil {
				return fmt.Errorf("restart %s: stop: %w", name, err)
			}
			if err := startVM(cmd.Context(), name); err != nil {
				return fmt.Errorf("restart %s: %w", name, err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "restarted %s\n", name)
			return nil
		},
	}
}

func deleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a VM (domain, OVN/OVS ports, state dir, ssh block)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]
			x, err := xenctl.New()
			if err != nil {
				return err
			}
			defer func() { _ = x.Close() }()
			nb, err := ovn.NewLive(ctx)
			if err != nil {
				return err
			}
			br, err := ovs.NewLive(ctx)
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("home dir: %w", err)
			}
			if err := xenctl.Delete(ctx, x, nb, br, home, vmDirOf(name), name); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", name)
			return nil
		},
	}
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List VMs: running from Xen, stopped from meta.yaml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			x, err := xenctl.New()
			if err != nil {
				return err
			}
			defer func() { _ = x.Close() }()
			infos, err := x.List()
			if err != nil {
				return err
			}
			metas := map[string]*vm.Meta{}
			entries, err := os.ReadDir(filepath.Join(installRoot, "vms"))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				m, err := vm.LoadMeta(filepath.Join(installRoot, "vms", e.Name()))
				if err != nil {
					continue // a crashed create may leave a dir without meta
				}
				metas[m.Name] = m
			}
			var stopped []*vm.Meta
			for _, m := range metas {
				running := false
				for _, d := range infos {
					if d.Name == m.Name {
						running = true
						break
					}
				}
				if !running {
					stopped = append(stopped, m)
				}
			}
			sort.Slice(stopped, func(i, j int) bool { return stopped[i].Name < stopped[j].Name })
			cmd.Print(listRows(infos, stopped, metas))
			return nil
		},
	}
}

// listRows renders `qlvm vm list` output: a header, one row per Xen domain
// (type from meta when known), then an "available" section with stopped VMs.
func listRows(infos []xenctl.DomainInfo, stopped []*vm.Meta, metas map[string]*vm.Meta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-16s %-12s %-10s %6s %6s\n", "NAME", "TYPE", "STATE", "MEM", "VCPUS")
	for _, d := range infos {
		typ := ""
		if m, ok := metas[d.Name]; ok {
			typ = m.Type
		}
		fmt.Fprintf(&b, "%-16s %-12s %-10s %6d %6d\n", d.Name, typ, d.State, d.MemMB, d.VCPUs)
	}
	if len(stopped) > 0 {
		b.WriteString("available:\n")
		for _, m := range stopped {
			fmt.Fprintf(&b, "%-16s %-12s %-10s %6d %6d\n", m.Name, m.Type, "stopped", m.MemoryMB, m.VCPUs)
		}
	}
	return b.String()
}

func init() {
	vmCmd().AddCommand(startCmd(), stopCmd(), killCmd(), restartCmd(), deleteCmd(), listCmd())
}
