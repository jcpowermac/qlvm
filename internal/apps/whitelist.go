package apps

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WhitelistFileName is the per-VM app whitelist inside the per-VM desktop
// cache directory, Qubes appmenus-style: one .desktop basename per line.
// Absent file = all apps visible; an empty list deletes the file.
const WhitelistFileName = "whitelisted-appmenus.list"

// AppEntry is one launchable cached desktop entry.
type AppEntry struct {
	Name string
	Base string // .desktop basename, e.g. "firefox.desktop"
	Icon string
	Exec string // field codes stripped, ready for waypipe
}

func vmCacheDir(cacheDir, vm string) string {
	return filepath.Join(cacheDir, vm)
}

// ReadWhitelist returns the whitelisted .desktop basenames, sorted and
// deduped. It returns nil (not an error) when the file is absent, and a
// possibly-empty non-nil slice when the file exists but is empty.
func ReadWhitelist(cacheDir, vm string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(vmCacheDir(cacheDir, vm), WhitelistFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read whitelist for %s: %w", vm, err)
	}
	names := sortedUniqueLines(string(raw))
	sort.Strings(names)
	if names == nil {
		names = []string{} // empty-but-present file: distinct from absent (nil)
	}
	return names, nil
}

// WriteWhitelist writes the whitelist sorted and deduped (0644 so the
// desktop user can read it). An empty list deletes the file, which is the
// "all apps visible" state.
func WriteWhitelist(cacheDir, vm string, names []string) error {
	path := filepath.Join(vmCacheDir(cacheDir, vm), WhitelistFileName)
	clean := sortedUniqueLines(strings.Join(names, "\n"))
	sort.Strings(clean)
	if len(clean) == 0 {
		err := os.Remove(path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete whitelist for %s: %w", vm, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache dir for %s: %w", vm, err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(clean, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("write whitelist for %s: %w", vm, err)
	}
	if err := os.Chmod(path, 0o644); err != nil { // umask-proof
		return fmt.Errorf("chmod whitelist for %s: %w", vm, err)
	}
	return nil
}

func sortedUniqueLines(s string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}

// CachedApps scans the per-VM desktop cache directory and returns every
// launchable entry (Type=Application, not NoDisplay, Name+Exec present),
// sorted by Name. It is the single source of truth for whitelist, tray,
// and rofi enumeration. A missing VM directory yields an empty result, not
// an error (never synced).
func CachedApps(cacheDir, vm string) ([]AppEntry, error) {
	dir := vmCacheDir(cacheDir, vm)
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read app cache for %s: %w", vm, err)
	}
	var out []AppEntry
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".desktop") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", de.Name(), err)
		}
		if DesktopKey(raw, "Type") != "Application" || DesktopKey(raw, "NoDisplay") == "true" {
			continue
		}
		name := DesktopKey(raw, "Name")
		exec := strings.TrimSpace(execFieldCodes.ReplaceAllString(DesktopKey(raw, "Exec"), ""))
		if name == "" || exec == "" {
			continue
		}
		icon := DesktopKey(raw, "Icon")
		if icon == "" {
			icon = DefaultIcon
		}
		out = append(out, AppEntry{Name: name, Base: de.Name(), Icon: icon, Exec: exec})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// VisibleApps returns the cached apps filtered through the per-VM whitelist
// (absent whitelist file = all apps). Matching is case-insensitive against
// the .desktop basename: Qubes writes lowercase entries while this cache
// uses Name-derived file names.
func VisibleApps(cacheDir, vm string) ([]AppEntry, error) {
	all, err := CachedApps(cacheDir, vm)
	if err != nil {
		return nil, err
	}
	wl, err := ReadWhitelist(cacheDir, vm)
	if err != nil {
		return nil, err
	}
	if wl == nil {
		return all, nil
	}
	allowed := make(map[string]bool, len(wl))
	for _, n := range wl {
		allowed[strings.ToLower(n)] = true
	}
	var out []AppEntry
	for _, a := range all {
		if allowed[strings.ToLower(a.Base)] {
			out = append(out, a)
		}
	}
	return out, nil
}

// SyncCache refreshes the per-VM desktop cache atomically and preserves any
// existing whitelist file across the WriteCache swap — the whitelist is user
// state, and a sync must not reset it to "all apps visible". Returns the
// number of desktop entries written.
func SyncCache(cacheDir, vm string, entries map[string]string) (int, error) {
	wl, err := ReadWhitelist(cacheDir, vm)
	if err != nil {
		return 0, err
	}
	n, err := WriteCache(cacheDir, vm, entries)
	if err != nil {
		return 0, err
	}
	if wl != nil {
		if err := WriteWhitelist(cacheDir, vm, wl); err != nil {
			return 0, err
		}
	}
	return n, nil
}
