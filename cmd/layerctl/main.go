// Command layerctl stages official Tellor Layer binaries into the cosmovisor
// layout and runs the node.
//
// Upgrades are meant to need no operator action at all:
//
//  1. governance passes an upgrade plan (e.g. "v6.1.8" at height 22460000)
//  2. the watcher notices the plan and downloads that official release,
//     verified against its published checksum, into upgrades/v6.1.8/bin/layerd
//  3. cosmovisor swaps binaries when the chain reaches the height
//
// Step 2 is the only part that was previously manual.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/cryptoriums/layer-docker/internal/cosmovisor"
	"github.com/cryptoriums/layer-docker/internal/plan"
	"github.com/cryptoriums/layer-docker/internal/release"
)

const (
	defaultHome         = "/root/chain"
	defaultPollInterval = 5 * time.Minute
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "run":
		err = run(ctx, os.Args[2:])
	case "stage":
		err = stage(ctx, os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}

	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "layerctl: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `layerctl - run Tellor Layer under cosmovisor using official release binaries

  layerctl run [-- <layerd args>]   stage the configured version, watch for
                                    upgrade plans, then exec cosmovisor
  layerctl stage <version>          download and stage one version, then exit

Environment:
  LAYER_HOME        node home (default /root/chain)
  LAYER_VERSION     version to run now, e.g. v6.1.8 (required on first start)
  LAYER_CHAIN_API   comma-separated chain REST endpoints for upgrade plans
  LAYER_POLL        how often to check for a scheduled upgrade (default 5m)
  LAYER_REPO        release repo (default tellor-io/layer)
  LAYER_NO_WATCH    set to 1 to disable automatic upgrade staging
`)
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// stage downloads one version into its cosmovisor upgrade directory.
func stage(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: layerctl stage <version>")
	}
	version := release.Version(args[0])
	if !version.Valid() {
		return fmt.Errorf("invalid version %q", args[0])
	}

	layout := cosmovisor.Layout{Home: env("LAYER_HOME", defaultHome)}
	if layout.Staged(version.Tag()) {
		fmt.Printf("%s is already staged\n", version.Tag())
		return nil
	}

	bin, err := release.New(env("LAYER_REPO", ""), 0).
		FetchBinary(ctx, version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := layout.InstallUpgrade(version.Tag(), bin); err != nil {
		return err
	}
	fmt.Printf("staged %s (%d bytes)\n", version.Tag(), len(bin))
	return nil
}

// run prepares the layout and execs cosmovisor, having first started the watcher
// that stages future upgrades.
func run(ctx context.Context, args []string) error {
	home := env("LAYER_HOME", defaultHome)
	layout := cosmovisor.Layout{Home: home}

	if err := layout.WriteConfig(); err != nil {
		return fmt.Errorf("write cosmovisor config: %w", err)
	}

	rel := release.New(env("LAYER_REPO", ""), 0)

	// Seed the genesis binary on first start. Once the node has upgraded at least
	// once, cosmovisor's own `current` symlink decides what runs, so this only
	// matters for a fresh home.
	if !layout.GenesisStaged() {
		version := release.Version(env("LAYER_VERSION", ""))
		if !version.Valid() {
			return fmt.Errorf("no genesis binary staged and LAYER_VERSION is unset or invalid")
		}
		fmt.Printf("layerctl: staging genesis binary %s\n", version.Tag())
		bin, err := rel.FetchBinary(ctx, version, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", version.Tag(), err)
		}
		if err := layout.InstallGenesis(bin); err != nil {
			return fmt.Errorf("install genesis: %w", err)
		}
	}

	// A configured LAYER_VERSION that is not yet staged as an upgrade is staged
	// too, so pinning a specific version in compose is enough to prepare it.
	if v := release.Version(env("LAYER_VERSION", "")); v.Valid() && !layout.Staged(v.Tag()) {
		if err := stageQuietly(ctx, rel, layout, v.Tag()); err != nil {
			fmt.Fprintf(os.Stderr, "layerctl: could not stage %s: %v\n", v.Tag(), err)
		}
	}

	if env("LAYER_NO_WATCH", "") != "1" {
		go watch(ctx, rel, layout)
	}

	return execCosmovisor(args)
}

// watch polls the chain for a scheduled upgrade and stages its binary.
func watch(ctx context.Context, rel *release.Client, layout cosmovisor.Layout) {
	endpoints := strings.Split(env("LAYER_CHAIN_API", "https://mainnet.tellorlayer.com"), ",")
	for i := range endpoints {
		endpoints[i] = strings.TrimSpace(endpoints[i])
	}
	planClient := plan.New(endpoints, 0)

	interval := defaultPollInterval
	if d, err := time.ParseDuration(env("LAYER_POLL", "")); err == nil && d > 0 {
		interval = d
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		checkPlan(ctx, planClient, rel, layout)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func checkPlan(ctx context.Context, planClient *plan.Client, rel *release.Client, layout cosmovisor.Layout) {
	p, err := planClient.Current(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "layerctl: upgrade plan check failed: %v\n", err)
		return
	}
	if p == nil {
		return
	}
	if layout.Staged(p.Name) {
		return
	}

	version := release.Version(p.Name)
	if !version.Valid() {
		// An upgrade whose name is not a release tag cannot be staged automatically.
		// Say so loudly: this is the case where a human must intervene before the
		// height, and silence would look identical to "nothing to do".
		fmt.Fprintf(os.Stderr,
			"layerctl: upgrade %q at height %d does not look like a release tag - stage it manually\n",
			p.Name, p.Height)
		return
	}

	fmt.Printf("layerctl: upgrade %s scheduled at height %d, staging binary\n", p.Name, p.Height)
	if err := stageQuietly(ctx, rel, layout, version.Tag()); err != nil {
		fmt.Fprintf(os.Stderr, "layerctl: staging %s failed: %v\n", version.Tag(), err)
		return
	}
	fmt.Printf("layerctl: %s staged, cosmovisor will switch at height %d\n", version.Tag(), p.Height)
}

func stageQuietly(ctx context.Context, rel *release.Client, layout cosmovisor.Layout, tag string) error {
	bin, err := rel.FetchBinary(ctx, release.Version(tag), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	return layout.InstallUpgrade(tag, bin)
}

// execCosmovisor replaces this process with cosmovisor, so signals and exit codes
// reach the node directly and nothing sits between Docker and the daemon.
func execCosmovisor(args []string) error {
	path, err := exec.LookPath("cosmovisor")
	if err != nil {
		return fmt.Errorf("cosmovisor not found: %w", err)
	}

	home := env("LAYER_HOME", defaultHome)
	envv := append(os.Environ(),
		"DAEMON_NAME=layerd",
		"DAEMON_HOME="+home,
		"DAEMON_RESTART_AFTER_UPGRADE=true",
		"DAEMON_ALLOW_DOWNLOAD_BINARIES=false",
	)

	argv := append([]string{"cosmovisor", "run"}, args...)
	return syscall.Exec(path, argv, envv)
}
