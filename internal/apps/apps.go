// Package apps is qlvm's desktop-file cache and rofi mode protocol: the
// VM's /usr/share/applications/*.desktop stream is fetched over SSH,
// split into per-app files under a dom0 cache dir, rendered as rofi menu
// lines, and launched (starting the VM first when it is stopped).
package apps

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// execFieldCodes is the freedesktop.org field-code set (%f, %F, %u, %U,
// %d, %D, %n, %N, %i, %c, %k, %v, %m) stripped from Exec= before launch.
var execFieldCodes = regexp.MustCompile(`%[fFuUdDnNickvm]`)

// DefaultIcon is the fallback rofi icon for cached apps without an
// Icon= key.
const DefaultIcon = "application-x-executable"

// DesktopGlob is the remote stream an app sync fetches: the VM shell
// expands the glob and cat concatenates the files (sshx.FetchFile). Shared
// by the CLI (apps sync) and the daemon (SyncApps).
const DesktopGlob = "/usr/share/applications/*.desktop"

// SplitDesktops splits a concatenated .desktop file stream into
// per-entry name→content. Entries begin at their [Desktop Entry] line
// and run to the next one; the key is the entry's Name= value, falling
// back to entry-<n> (1-based stream order) when the entry has none.
func SplitDesktops(data []byte) (map[string]string, error) {
	entries := map[string]string{}
	pos, n := 0, 0
	for {
		i := strings.Index(string(data[pos:]), "[Desktop Entry]")
		if i < 0 {
			break
		}
		start := pos + i
		n++
		end := len(data)
		if j := strings.Index(string(data[start+1:]), "[Desktop Entry]"); j >= 0 {
			end = start + 1 + j
		}
		content := data[start:end]
		name := DesktopKey(content, "Name")
		if name == "" {
			name = fmt.Sprintf("entry-%d", n)
		}
		entries[name] = string(content)
		pos = start + 1
	}
	if len(entries) == 0 {
		return nil, errors.New("no [Desktop Entry] sections in stream")
	}
	return entries, nil
}

// DesktopKey returns the value of the first "Key=" line of a desktop
// entry's content (surrounding quotes and CR stripped); "" when absent.
func DesktopKey(content []byte, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		v := strings.TrimSpace(line[len(prefix):])
		if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
			v = v[1 : len(v)-1]
		}
		return v
	}
	return ""
}

// cacheName makes a desktop Name safe as a .desktop file name on dom0.
func cacheName(name string) string {
	return strings.NewReplacer("/", "_", "\x00", "_").Replace(name)
}

// WriteCache replaces the VM's desktop cache at <cacheDir>/<vm> with
// entries — one <name>.desktop per entry — and returns the count written.
// The new set is built in a temp sibling dir and swapped in whole, so a
// mid-write failure leaves the previous cache (if any) intact.
func WriteCache(cacheDir, vm string, entries map[string]string) (int, error) {
	dir := filepath.Join(cacheDir, vm)
	tmp := dir + ".tmp"
	if err := os.RemoveAll(tmp); err != nil { // a crash may leave a stale temp dir
		return 0, err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return 0, err
	}
	if err := os.Chmod(tmp, 0o755); err != nil { // umask-proof: desktop user must traverse
		_ = os.RemoveAll(tmp)
		return 0, err
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(tmp, cacheName(name)+".desktop")
		if err := os.WriteFile(path, []byte(entries[name]), 0o644); err != nil {
			_ = os.RemoveAll(tmp)
			return 0, err
		}
		if err := os.Chmod(path, 0o644); err != nil { // umask-proof: desktop user must read
			_ = os.RemoveAll(tmp)
			return 0, err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		_ = os.RemoveAll(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		return 0, err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		_ = os.RemoveAll(tmp)
		return 0, err
	}
	return len(entries), nil
}

// EmitRofi renders the rofi mode protocol output from the whole cache:
// one line per launchable cached app (see CachedApps) that passes the
// per-VM whitelist filter (absent whitelist file = all apps) —
//
//	[<vm>] <name>\x00icon\x1f<icon|application-x-executable>\x1finfo\x1f<vm>|<exec>\n
//
// with exec field codes (%f, %U, ...) stripped and lines sorted. A
// missing cache dir yields an empty menu.
func EmitRofi(cacheDir string) (string, error) {
	vmDirs, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var lines []string
	for _, d := range vmDirs {
		if !d.IsDir() {
			continue
		}
		entries, err := VisibleApps(cacheDir, d.Name())
		if err != nil {
			return "", err
		}
		for _, a := range entries {
			lines = append(lines, fmt.Sprintf("[%s] %s\x00icon\x1f%s\x1finfo\x1f%s|%s\n", d.Name(), a.Name, a.Icon, d.Name(), a.Exec))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, ""), nil
}

// Launch runs a rofi selection: rofiInfo is the line's info field
// "<vm>|<exec>". A stopped VM is started and waited on for SSH before
// the app is run; a running VM goes straight to runApp.
func Launch(rofiInfo, cacheDir string, running func(vm string) bool, start func(vm string) error, waitSSH func(vm string) error, runApp func(vm, exec string) error) error { //nolint:revive // cacheDir is part of the Launch contract (Task 13); unused for now
	vmName, exec, ok := strings.Cut(rofiInfo, "|")
	if !ok || vmName == "" || exec == "" {
		return fmt.Errorf("bad rofi info %q: want <vm>|<exec>", rofiInfo)
	}
	if !running(vmName) {
		if err := start(vmName); err != nil {
			return err
		}
		if err := waitSSH(vmName); err != nil {
			return err
		}
	}
	return runApp(vmName, exec)
}
