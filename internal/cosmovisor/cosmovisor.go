// Package cosmovisor manages the on-disk layout cosmovisor expects, so an upgrade
// is nothing more than "the right binary is already in the right directory before
// the chain reaches the upgrade height".
//
// Layout under <home>/cosmovisor:
//
//	genesis/bin/layerd            the version the node starts on
//	upgrades/<plan>/bin/layerd    one directory per upgrade, named after the plan
//	current -> genesis | upgrades/<plan>   maintained by cosmovisor itself
//
// The plan name is the governance proposal's upgrade name, which for Tellor is the
// release tag ("v6.1.8"), so a staged directory needs no translation table.
package cosmovisor

import (
	"fmt"
	"os"
	"path/filepath"
)

// Layout describes the cosmovisor directories under a node home.
type Layout struct {
	Home string // node home, e.g. /root/chain
}

// Root is <home>/cosmovisor.
func (l Layout) Root() string { return filepath.Join(l.Home, "cosmovisor") }

// GenesisBin is the directory holding the initial binary.
func (l Layout) GenesisBin() string { return filepath.Join(l.Root(), "genesis", "bin") }

// UpgradeBin is the directory holding the binary for a named upgrade plan.
func (l Layout) UpgradeBin(plan string) string {
	return filepath.Join(l.Root(), "upgrades", plan, "bin")
}

// Staged reports whether a runnable binary is already in place for plan.
func (l Layout) Staged(plan string) bool {
	return executable(filepath.Join(l.UpgradeBin(plan), "layerd"))
}

// GenesisStaged reports whether the genesis binary is in place.
func (l Layout) GenesisStaged() bool {
	return executable(filepath.Join(l.GenesisBin(), "layerd"))
}

func executable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() || info.Size() == 0 {
		return false
	}
	return info.Mode()&0o111 != 0
}

// InstallGenesis writes bin as the genesis binary, replacing any existing one.
func (l Layout) InstallGenesis(bin []byte) error {
	return install(filepath.Join(l.GenesisBin(), "layerd"), bin)
}

// InstallUpgrade writes bin as the binary for plan.
//
// It never overwrites an already-staged upgrade. Once a plan directory holds a
// binary, that is the artifact the node has been prepared to run; silently
// swapping it underneath could change what executes at the upgrade height.
func (l Layout) InstallUpgrade(plan string, bin []byte) error {
	if l.Staged(plan) {
		return fmt.Errorf("upgrade %q is already staged", plan)
	}
	return install(filepath.Join(l.UpgradeBin(plan), "layerd"), bin)
}

// install writes bin to path atomically: a temp file in the same directory, then a
// rename. A half-written binary is never visible to cosmovisor, which may be
// scanning for it at any moment.
func install(path string, bin []byte) error {
	if len(bin) == 0 {
		return fmt.Errorf("refusing to install an empty binary at %s", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".layerd-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(bin); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename to %s: %w", path, err)
	}
	return nil
}

// WriteConfig writes cosmovisor's config.toml if it is absent.
//
// daemon_allow_download_binaries stays false on purpose. Tellor's upgrade plans
// carry an empty info field, so cosmovisor has no URL to fetch anyway, and letting
// a validator download and execute an arbitrary binary named by an on-chain
// message is not a property we want. This image stages binaries itself, from the
// official releases, verified by their published checksums.
func (l Layout) WriteConfig() error {
	path := filepath.Join(l.Root(), "config.toml")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(l.Root(), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf(`# Managed by layerctl. Binaries are staged from official releases, not downloaded
# by cosmovisor, so downloads stay disabled.
daemon_home = %q
daemon_name = "layerd"
daemon_allow_download_binaries = false
daemon_restart_after_upgrade = true
daemon_poll_interval = "300ms"
unsafe_skip_backup = true
`, l.Home)
	return os.WriteFile(path, []byte(content), 0o644)
}
