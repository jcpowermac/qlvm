package provisioner

import "testing"

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
