package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SSHUser is the fixed user baked into every VM's image by the ostree bake
// (Task 8); ssh-config blocks and direct VM connections both use it.
const SSHUser = "user"

// sshBlock renders one ssh config block (supervisor ruling 2026-09-26; the
// "spec §6.5" the brief cites is not in this repo): app VMs get a Host
// block, disposable do not. No known-hosts directives: sshx's Go SSH uses
// InsecureIgnoreHostKey and waypipe ssh inherits this file.
func sshBlock(name, ip string) string {
	return "Host " + name + "\n  HostName " + ip + "\n  User " + SSHUser
}

func sshConfigPath(home string) string {
	return filepath.Join(home, ".ssh", "config")
}

// parseBlocks splits an ssh config into blank-line-separated blocks.
func parseBlocks(content string) []string {
	var blocks, cur []string
	for _, line := range strings.Split(content, "\n") {
		if line == "" {
			if len(cur) > 0 {
				blocks = append(blocks, strings.Join(cur, "\n"))
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		blocks = append(blocks, strings.Join(cur, "\n"))
	}
	return blocks
}

// blockName returns the first host name of a "Host ..." line.
// ponytail: multi-name "Host a b" lists only match the first name; qlvm only
// ever writes single-name blocks, and rewriting a hand-edited list is riskier.
func blockName(block string) (string, bool) {
	line := block
	if i := strings.IndexByte(block, '\n'); i >= 0 {
		line = block[:i]
	}
	fields := strings.Fields(line)
	if len(fields) >= 2 && fields[0] == "Host" {
		return fields[1], true
	}
	return "", false
}

func writeConfig(path string, blocks []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(blocks, "\n\n")+"\n"), 0o600)
}

// SSHEntry returns the HostName and User for host in home's ~/.ssh/config
// (User is "" when the block has no User line). Error when the file or
// block is missing.
func SSHEntry(home, host string) (string, string, error) {
	path := sshConfigPath(home)
	data, err := os.ReadFile(path) // #nosec G304 -- path is the caller-provided ssh config directory
	if err != nil {
		return "", "", err
	}
	for _, b := range parseBlocks(string(data)) {
		name, ok := blockName(b)
		if !ok || name != host {
			continue
		}
		hostname, user := blockEntry(b)
		return hostname, user, nil
	}
	return "", "", fmt.Errorf("no Host %s block in %s", host, path)
}

// blockEntry picks the HostName/User fields out of one ssh config block.
func blockEntry(block string) (string, string) {
	var hostname, user string
	for _, line := range strings.Split(block, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "hostname":
			hostname = fields[1]
		case "user":
			user = fields[1]
		}
	}
	return hostname, user
}

// AddSSHConfig idempotently adds (or replaces, in place) the Host block for m.
func AddSSHConfig(home string, m *Meta) error {
	path := sshConfigPath(home)
	data, err := os.ReadFile(path) // #nosec G304 -- path is the caller-provided ssh config directory
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	blocks := parseBlocks(string(data))
	block := sshBlock(m.Name, m.IP)
	replaced := false
	for i, b := range blocks {
		if name, ok := blockName(b); ok && name == m.Name {
			blocks[i] = block
			replaced = true
			break
		}
	}
	if !replaced {
		blocks = append(blocks, block)
	}
	return writeConfig(path, blocks)
}

// RemoveSSHConfig deletes the Host block for name (no-op if absent).
func RemoveSSHConfig(home, name string) error {
	path := sshConfigPath(home)
	data, err := os.ReadFile(path) // #nosec G304 -- path is the caller-provided ssh config directory
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var out []string
	for _, b := range parseBlocks(string(data)) {
		if n, ok := blockName(b); ok && n == name {
			continue
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writeConfig(path, out)
}
