package ostree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildGPT writes a minimal GPT disk (protective-MBR LBA, header at LBA1,
// 16 entries at LBA2) with the given partition GUIDs.
func buildGPT(t *testing.T, path string, guids ...[16]byte) {
	t.Helper()
	img := make([]byte, 2048+16*128)
	copy(img[512:520], "EFI PART")
	img[524] = 92 // header size
	off := 1024
	typeGUID := [16]byte{0xaf, 0x3d, 0xc2, 0x26, 0x1f, 0x8c, 0x4f, 0xc5}
	for _, g := range guids {
		// A zero partition GUID stands for an empty (all-zero) slot.
		if g == [16]byte{} {
			off += 128
			continue
		}
		e := gptEntry(typeGUID, g)
		copy(img[off:off+128], e)
		off += 128
	}
	require.NoError(t, os.WriteFile(path, img, 0o600))
}

func TestGptPartUUID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "disk.raw")
	guid := [16]byte{0x91, 0x8e, 0xa0, 0x51, 0x18, 0x4f, 0xe7, 0x4f,
		0x8c, 0xa1, 0x68, 0xc7, 0xa8, 0x7c, 0xda, 0x98}
	buildGPT(t, path, [16]byte{}, guid)

	got, err := gptPartUUID(path, 2)
	require.NoError(t, err)
	assert.Equal(t, "918ea051-184f-e74f-8ca1-68c7a87cda98", got)

	_, err = gptPartUUID(path, 1)
	assert.Error(t, err, "empty GPT entry must not yield a GUID")
}
