package ostree

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
)

// gptPartUUID returns the partition GUID (PARTUUID) for 1-based partition
// number n of the GPT disk at path. Pure Go: the kernel does not expose
// partition UUIDs in sysfs for loop-backed disks (blkid reads them from
// the GPT, and so does this).
func gptPartUUID(path string, n int) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- caller-owned template/disk path
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	sig := make([]byte, 8)
	if _, err := f.ReadAt(sig, 512); err != nil {
		return "", fmt.Errorf("read GPT header: %w", err)
	}
	if string(sig) != "EFI PART" {
		return "", fmt.Errorf("%s: not a GPT disk (bad header signature)", path)
	}

	const entrySize = 128
	entries := make([]byte, 16*entrySize)
	if _, err := f.ReadAt(entries, 1024); err != nil {
		return "", fmt.Errorf("read GPT entries: %w", err)
	}
	if n < 1 || n > 16 {
		return "", fmt.Errorf("partition %d out of range", n)
	}
	e := entries[(n-1)*entrySize : (n-1)*entrySize+entrySize]
	var typeGUID [16]byte
	copy(typeGUID[:], e[0:16])
	if typeGUID == [16]byte{} {
		return "", fmt.Errorf("partition %d: empty GPT entry", n)
	}
	// Partition GUID at offset 16. GPT stores the first three fields
	// little-endian (4+2+2 bytes) and the last two big-endian — unlike the
	// RFC 4122 mixed-endian uuid.UUID layout, so canonicalize explicitly.
	var b [16]byte
	copy(b[:], e[16:32])
	c := [16]byte{
		b[3], b[2], b[1], b[0],
		b[5], b[4],
		b[7], b[6],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15],
	}
	return strings.ToLower(fmt.Sprintf("%x-%x-%x-%x-%x", c[0:4], c[4:6], c[6:8], c[8:10], c[10:16])), nil
}

// gptEntry builds one 128-byte GPT partition entry (for tests). partGUID
// is in canonical RFC 4122 byte order; the entry stores it in GPT
// on-disk order (first three fields little-endian, last two big-endian).
func gptEntry(typeGUID, partGUID [16]byte) []byte {
	e := make([]byte, 128)
	copy(e[0:16], typeGUID[:])
	copy(e[16:20], []byte{partGUID[3], partGUID[2], partGUID[1], partGUID[0]})
	copy(e[20:22], []byte{partGUID[5], partGUID[4]})
	copy(e[22:24], []byte{partGUID[7], partGUID[6]})
	copy(e[24:32], partGUID[8:16])
	binary.LittleEndian.PutUint64(e[32:40], 2048)
	binary.LittleEndian.PutUint64(e[40:48], 4096)
	return e
}
