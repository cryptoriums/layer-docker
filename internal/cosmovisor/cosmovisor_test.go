package cosmovisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayoutPaths(t *testing.T) {
	l := Layout{Home: "/root/chain"}
	cases := map[string]string{
		l.Root():               "/root/chain/cosmovisor",
		l.GenesisBin():         "/root/chain/cosmovisor/genesis/bin",
		l.UpgradeBin("v6.1.8"): "/root/chain/cosmovisor/upgrades/v6.1.8/bin",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}

func TestInstallUpgradeWritesExecutable(t *testing.T) {
	l := Layout{Home: t.TempDir()}

	if l.Staged("v6.1.8") {
		t.Fatal("nothing should be staged in a fresh home")
	}
	if err := l.InstallUpgrade("v6.1.8", []byte("binary")); err != nil {
		t.Fatalf("InstallUpgrade: %v", err)
	}
	if !l.Staged("v6.1.8") {
		t.Fatal("Staged should report true after install")
	}

	path := filepath.Join(l.UpgradeBin("v6.1.8"), "layerd")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("mode = %v, want the executable bit set", info.Mode())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "binary" {
		t.Fatalf("content = %q, want %q", content, "binary")
	}
}

// Re-staging must not silently replace a binary the node has already been
// prepared to run at a known height.
func TestInstallUpgradeRefusesToOverwrite(t *testing.T) {
	l := Layout{Home: t.TempDir()}
	if err := l.InstallUpgrade("v6.1.8", []byte("first")); err != nil {
		t.Fatalf("first install: %v", err)
	}
	err := l.InstallUpgrade("v6.1.8", []byte("second"))
	if err == nil {
		t.Fatal("expected an error when re-staging an existing upgrade")
	}
	if !strings.Contains(err.Error(), "already staged") {
		t.Fatalf("error = %v, want 'already staged'", err)
	}
	content, _ := os.ReadFile(filepath.Join(l.UpgradeBin("v6.1.8"), "layerd"))
	if string(content) != "first" {
		t.Fatalf("content = %q, the original binary must survive", content)
	}
}

func TestInstallRejectsEmptyBinary(t *testing.T) {
	l := Layout{Home: t.TempDir()}
	if err := l.InstallUpgrade("v6.1.8", nil); err == nil {
		t.Fatal("expected an error for an empty binary")
	}
	if l.Staged("v6.1.8") {
		t.Fatal("nothing should be staged after a rejected install")
	}
}

// A zero-length or non-executable file left behind by a failed copy must not be
// mistaken for a staged upgrade, or the node reaches the height with nothing to run.
func TestStagedIgnoresUnusableFiles(t *testing.T) {
	l := Layout{Home: t.TempDir()}
	dir := l.UpgradeBin("v6.1.9")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "layerd")

	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if l.Staged("v6.1.9") {
		t.Error("an empty file must not count as staged")
	}

	// os.WriteFile keeps an existing file's mode, so set it explicitly.
	if err := os.WriteFile(path, []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if l.Staged("v6.1.9") {
		t.Error("a non-executable file must not count as staged")
	}
}

func TestWriteConfigIsIdempotent(t *testing.T) {
	l := Layout{Home: t.TempDir()}
	if err := l.WriteConfig(); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	path := filepath.Join(l.Root(), "config.toml")
	if err := os.WriteFile(path, []byte("# operator edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.WriteConfig(); err != nil {
		t.Fatalf("second WriteConfig: %v", err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "# operator edited\n" {
		t.Fatal("WriteConfig must not overwrite an existing config")
	}
}

func TestWriteConfigDisablesDownloads(t *testing.T) {
	l := Layout{Home: t.TempDir()}
	if err := l.WriteConfig(); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(l.Root(), "config.toml"))
	if !strings.Contains(string(content), "daemon_allow_download_binaries = false") {
		t.Fatal("cosmovisor must not be allowed to download binaries itself")
	}
}
