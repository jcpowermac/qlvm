package template

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// bakeAgentTemplate extracts kernel/initramfs from an agent container image
// and writes them to dir. Returns the Template metadata.
func bakeAgentTemplate(ctx context.Context, pc Podman, ref, dir string, log io.Writer) (*Template, error) {
	// Inspect image for labels
	labels, err := pc.InspectImage(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("inspect image: %w", err)
	}
	kernelPath, ok := labels["io.qlvm.kernel"]
	if !ok {
		kernelPath = defaultAgentKernelPath(labels)
	}
	initramfsPath, ok := labels["io.qlvm.initramfs"]
	if !ok {
		initramfsPath = defaultAgentInitramfsPath(labels)
	}

	// Create temporary container for file extraction
	containerID, err := pc.CreateContainer(ctx, ref, nil)
	if err != nil {
		return nil, fmt.Errorf("create extraction container: %w", err)
	}
	defer func() {
		_ = pc.RemoveContainer(ctx, containerID, true)
	}()

	// Extract kernel version
	kernelVer, err := extractString(pc, containerID, "/etc/kernel-version")
	if err != nil {
		return nil, fmt.Errorf("extract kernel version: %w", err)
	}
	kernelVer = strings.TrimSpace(kernelVer)

	// Extract kernel
	kernelData, err := extractFile(pc, containerID, kernelPath)
	if err != nil {
		return nil, fmt.Errorf("extract kernel: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vmlinuz"), kernelData, 0o644); err != nil {
		return nil, fmt.Errorf("write vmlinuz: %w", err)
	}

	// Extract initramfs
	initramfsData, err := extractFile(pc, containerID, initramfsPath)
	if err != nil {
		return nil, fmt.Errorf("extract initramfs: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "initramfs"), initramfsData, 0o644); err != nil {
		return nil, fmt.Errorf("write initramfs: %w", err)
	}

	digest, err := pc.Pull(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("pull image for digest: %w", err)
	}

	return &Template{
		Dir:       dir,
		Image:     ref,
		Digest:    digest,
		KernelVer: kernelVer,
		Type:      "agent",
	}, nil
}

// extractFile extracts a file from a container via CopyToArchive.
// Returns the file content as bytes (binary-safe for kernel/initramfs).
func extractFile(pc Podman, containerID, path string) ([]byte, error) {
	var buf bytes.Buffer
	err := pc.CopyFromContainer(context.Background(), containerID, path, &buf)
	if err != nil {
		return nil, err
	}
	// CopyToArchive returns a tar archive; extract the first regular file
	tr := tar.NewReader(&buf)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("no file found in archive for %s", path)
}

// extractString is a helper for text files (kernel-version).
func extractString(pc Podman, containerID, path string) (string, error) {
	data, err := extractFile(pc, containerID, path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// defaultAgentKernelPath derives kernel path from version label.
func defaultAgentKernelPath(labels map[string]string) string {
	ver := labels["org.opencontainers.image.version"]
	return fmt.Sprintf("/usr/lib/modules/%s/vmlinuz", ver)
}

// defaultAgentInitramfsPath derives initramfs path from version label.
func defaultAgentInitramfsPath(labels map[string]string) string {
	ver := labels["org.opencontainers.image.version"]
	return fmt.Sprintf("/usr/lib/modules/%s/initramfs.img", ver)
}
