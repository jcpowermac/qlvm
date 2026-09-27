package provisioner

import "testing"

func TestDnfInstallCmd(t *testing.T) {
	got, err := dnfInstallCmd([]string{"vim", "git-core", "python3-pip"})
	if err != nil {
		t.Fatal(err)
	}
	want := "sudo dnf install -y vim git-core python3-pip"
	if got != want {
		t.Fatalf("dnfInstallCmd = %q, want %q", got, want)
	}

	if _, err := dnfInstallCmd(nil); err != nil {
		t.Fatalf("empty list: %v", err)
	}

	for _, bad := range []string{"vim;reboot", "a b", "x`id`", ""} {
		if _, err := dnfInstallCmd([]string{bad}); err == nil {
			t.Fatalf("dnfInstallCmd(%q) accepted an unsafe name", bad)
		}
	}
}

func TestUploadPathSafety(t *testing.T) {
	for _, bad := range []string{"/etc/passwd", "../escape", "a/../../etc", ""} {
		op := Op{Kind: KindFile, Path: bad}
		remote, ok := safeRemotePath(op.Path)
		if ok {
			t.Fatalf("unsafe path %q mapped to %q", bad, remote)
		}
	}
	remote, ok := safeRemotePath(".config/starship.toml")
	if !ok || remote != "/home/user/.config/starship.toml" {
		t.Fatalf("safeRemotePath = %q, %v; want /home/user/.config/starship.toml", remote, ok)
	}
}
