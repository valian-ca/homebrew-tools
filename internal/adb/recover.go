// Package adb handles recovery of the adb daemon when it hangs or loses
// sight of wireless devices.
package adb

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Restart kills any running adb process and starts a fresh server.
// Output goes to w (typically os.Stderr) so the user sees progress.
func Restart(ctx context.Context, w io.Writer) error {
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not in PATH")
	}
	fmt.Fprintln(w, "Restarting adb...")
	// pkill -9 is best-effort: nothing to do if adb isn't running.
	_ = exec.Command("pkill", "-9", "adb").Run()

	startCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(startCtx, "adb", "start-server").Run(); err != nil {
		fmt.Fprintln(w, "adb start-server timed out.")
		return err
	}
	return nil
}

// mdns services output line: "<service-name>\t<type>\t<addr:port>". We want
// the final column (the address) for services whose type is
// _adb-tls-connect. "tail -r" in bash reverses so the most recent
// advertisement wins; we reverse in-place here.
var mdnsTLSLine = regexp.MustCompile(`_adb-tls-connect`)

// ReconnectWireless mirrors the zsh `adb-reconnect` helper: drop stale
// tcp connections, rediscover via mDNS, reconnect to each advertised
// service until one answers "connected". Returns the address that
// connected, or an error if none did.
func ReconnectWireless(ctx context.Context, w io.Writer) (string, error) {
	if _, err := exec.LookPath("adb"); err != nil {
		return "", fmt.Errorf("adb not in PATH")
	}

	dropStale(ctx, w)

	addrs := discoverMDNS(ctx)
	if len(addrs) == 0 {
		fmt.Fprintln(w, "No wireless services found, restarting adb...")
		if err := Restart(ctx, w); err != nil {
			return "", err
		}
		time.Sleep(1 * time.Second)
		addrs = discoverMDNS(ctx)
	}
	if len(addrs) == 0 {
		fmt.Fprintln(w, "No wireless debugging service found. Is it enabled on your phone?")
		return "", fmt.Errorf("no wireless services")
	}
	for _, addr := range addrs {
		fmt.Fprintf(w, "Trying %s...\n", addr)
		connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, err := exec.CommandContext(connectCtx, "adb", "connect", addr).CombinedOutput()
		cancel()
		if err == nil && strings.Contains(string(out), "connected") {
			fmt.Fprintf(w, "Connected to %s\n", addr)
			return addr, nil
		}
	}
	return "", fmt.Errorf("failed to connect to any discovered service")
}

// dropStale disconnects any ip:port connections currently listed by
// `adb devices`. `adb devices` lists entries like "192.168.1.5:5555".
// Unlike the bash version which matched /^[0-9]+\./, we use a stricter
// "contains ':'" heuristic — same practical effect for ipv4.
func dropStale(ctx context.Context, w io.Writer) {
	lsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(lsCtx, "adb", "devices").Output()
	if err != nil {
		return
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if first {
			first = false
			continue
		}
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		serial := fields[0]
		if !strings.Contains(serial, ":") {
			continue
		}
		fmt.Fprintf(w, "Disconnecting stale %s...\n", serial)
		dcCtx, cancelDC := context.WithTimeout(ctx, 5*time.Second)
		_ = exec.CommandContext(dcCtx, "adb", "disconnect", serial).Run()
		cancelDC()
	}
}

// discoverMDNS returns the deduplicated list of _adb-tls-connect addresses.
// Newest (last-advertised) first, matching the bash "tail -r" ordering.
func discoverMDNS(ctx context.Context) []string {
	mdnsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(mdnsCtx, "adb", "mdns", "services").Output()
	if err != nil {
		return nil
	}
	var addrs []string
	seen := make(map[string]struct{})
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if !mdnsTLSLine.MatchString(line) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		addr := fields[len(fields)-1]
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		addrs = append(addrs, addr)
	}
	// Reverse to match bash `tail -r`.
	for i, j := 0, len(addrs)-1; i < j; i, j = i+1, j-1 {
		addrs[i], addrs[j] = addrs[j], addrs[i]
	}
	return addrs
}
