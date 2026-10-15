package template

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBakeAgentTemplate(t *testing.T) {
	dir := t.TempDir()
	pc := &fakePodman{
		digest: "sha256:abcd1234",
		inspectFunc: func(_ context.Context, ref string) (map[string]string, error) {
			return map[string]string{
				"io.qlvm.type":      "pvh-initramfs",
				"io.qlvm.kernel":    "/usr/lib/modules/7.2.8-xen1/vmlinuz",
				"io.qlvm.initramfs": "/usr/lib/modules/7.2.8-xen1/initramfs.img",
			}, nil
		},
		createFunc: func(_ context.Context, ref string, cmd []string) (string, error) {
			return "container123", nil
		},
		copyFunc: func(_ context.Context, id, path string, writer io.Writer) error {
			var content []byte
			if strings.Contains(path, "vmlinuz") {
				content = []byte("fake-kernel")
			} else if strings.Contains(path, "initramfs") {
				content = []byte("fake-initramfs")
			} else if strings.Contains(path, "kernel-version") {
				content = []byte("7.2.8-xen1")
			}
			// Write as tar archive (matching real CopyToArchive behavior)
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			_ = tw.WriteHeader(&tar.Header{
				Name:     filepath.Base(path),
				Mode:     0o644,
				Size:     int64(len(content)),
				Typeflag: tar.TypeReg,
			})
			_, _ = tw.Write(content)
			_ = tw.Close()
			_, _ = writer.Write(buf.Bytes())
			return nil
		},
		removeContErr: nil,
	}
	labels := map[string]string{
		"io.qlvm.type":      "pvh-initramfs",
		"io.qlvm.kernel":    "/usr/lib/modules/7.2.8-xen1/vmlinuz",
		"io.qlvm.initramfs": "/usr/lib/modules/7.2.8-xen1/initramfs.img",
		"PRETTY_NAME":       "Alpine Linux",
	}
	tpl, err := bakeAgentTemplate(context.Background(), pc, "test-image", "sha256:abcd1234", labels, dir, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tpl.KernelVer != "7.2.8-xen1" {
		t.Errorf("expected kernel version 7.2.8-xen1, got %s", tpl.KernelVer)
	}
	if _, err := os.Stat(filepath.Join(dir, "vmlinuz")); err != nil {
		t.Errorf("vmlinuz not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "initramfs")); err != nil {
		t.Errorf("initramfs not found: %v", err)
	}
}
