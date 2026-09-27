package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jcpowermac/qlvm/internal/config"
	"github.com/jcpowermac/qlvm/internal/fw"
	"github.com/jcpowermac/qlvm/internal/nm"
	"github.com/jcpowermac/qlvm/internal/ovn"
	"github.com/jcpowermac/qlvm/internal/ovs"
	"github.com/jcpowermac/qlvm/internal/setup"
	"github.com/jcpowermac/qlvm/internal/systemd"
)

// defaultConfigPath is where install reads and rewrites the config.
const defaultConfigPath = "/etc/qvm/qlvm.toml"

// installRoot is the per-VM/template state tree (spec §5.2).
const installRoot = "/var/lib/qvm"

func installCmd() *cobra.Command {
	var configPath string
	var skipNICMigration bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Drive the dom0 toward the state declared by the config file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			plan, err := newInstallPlan(cfg, configPath)
			if err != nil {
				return err
			}
			return setup.Run(cmd.Context(), plan, cfg, setup.Options{SkipNICMigration: skipNICMigration})
		},
	}
	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath, "path to qlvm.toml")
	cmd.Flags().BoolVar(&skipNICMigration, "skip-nic-migration", false, "allow the NIC migration to proceed during an SSH session")
	return cmd
}

// newInstallPlan wires the real control planes into a setup.Plan.
func newInstallPlan(cfg *config.Config, configPath string) (*setup.Plan, error) {
	fwMgr, err := fw.NewSystem()
	if err != nil {
		return nil, err
	}
	nmMgr, err := nm.NewSystem()
	if err != nil {
		return nil, err
	}
	sdMgr, err := systemd.NewSession()
	if err != nil {
		return nil, err
	}
	nb, err := ovn.NewLive(context.Background())
	if err != nil {
		return nil, err
	}
	ovsDB, err := ovs.NewLive(context.Background())
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &setup.Plan{
		OVN: nb,
		OVS: ovsDB,
		FW:  fwMgr,
		NM:  nmMgr,
		SD:  sdMgr,
		// ponytail: plain-dir fallback (spec §5.2); upgrade to a btrfs
		// subvolume via an x/sys BTRFS_IOC_SUBVOL_CREATE ioctl if the
		// subvolume's snapshot/usage isolation ever matters.
		Storage: func() error {
			for _, dir := range []string{installRoot, installRoot + "/templates", installRoot + "/vms"} {
				if err := os.MkdirAll(dir, 0o750); err != nil {
					return err
				}
			}
			return nil
		},
		VifScript: func() error {
			return InstallVifScript("/etc/xen/scripts/vif-ovn", filepath.Join(filepath.Dir(exe), "qlvm-vif"))
		},
		SaveCfg: func() error {
			if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
				return err
			}
			return cfg.Save(configPath)
		},
	}, nil
}

func init() {
	NewRootCmd().AddCommand(installCmd())
}
