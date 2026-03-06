package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	egressModeOpen        = "open"
	egressModeSafeDefault = "safe-default"
	egressModeAllowlist   = "allowlist"
)

var errContainerNotFound = errors.New("container not found")

var safeDefaultBlockedIPv4 = []string{
	"127.0.0.0/8",
	"169.254.169.254/32",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

var safeDefaultBlockedIPv6 = []string{
	"::1/128",
	"fc00::/7",
	"fe80::/10",
}

type resolvedAllowlist struct {
	IPv4 []string
	IPv6 []string
}

type egressRuleHandle struct {
	binary string
	chain  string
	source string
}

func normalizeEgressMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return egressModeOpen
	}
	return mode
}

func applyContainerEgressPolicy(ctx context.Context, containerName, runID, mode string, allowlist []string, logOut io.Writer) (func(), error) {
	mode = normalizeEgressMode(mode)
	if mode == egressModeOpen {
		return func() {}, nil
	}

	addrs, running, err := waitForContainerAddresses(ctx, containerName, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("resolve container addresses: %w", err)
	}
	if !running || len(addrs) == 0 {
		_, _ = fmt.Fprintf(logOut, "[%s] egress policy mode=%s skipped (container exited before policy apply)\n", time.Now().UTC().Format(time.RFC3339), mode)
		return func() {}, nil
	}

	var resolved resolvedAllowlist
	if mode == egressModeAllowlist {
		resolved, err = resolveAllowlist(ctx, allowlist)
		if err != nil {
			return nil, err
		}
	}

	handles := make([]egressRuleHandle, 0, len(addrs))
	cleanup := func() {
		for i := len(handles) - 1; i >= 0; i-- {
			handles[i].cleanup(logOut)
		}
	}

	for _, addr := range addrs {
		h, err := installEgressRulesForAddress(addr, runID, mode, resolved, logOut)
		if err != nil {
			cleanup()
			return nil, err
		}
		handles = append(handles, h)
	}

	addrText := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		addrText = append(addrText, addr.String())
	}
	sort.Strings(addrText)
	_, _ = fmt.Fprintf(logOut, "[%s] egress policy mode=%s applied run_id=%s addrs=%s\n", time.Now().UTC().Format(time.RFC3339), mode, runID, strings.Join(addrText, ","))

	return cleanup, nil
}

func waitForContainerAddresses(ctx context.Context, containerName string, timeout time.Duration) ([]netip.Addr, bool, error) {
	deadline := time.Now().Add(timeout)
	sawContainer := false
	for {
		running, addrs, err := inspectContainerAddresses(containerName)
		switch {
		case err == nil && running && len(addrs) > 0:
			return addrs, true, nil
		case err == nil && !running:
			return nil, false, nil
		case err == nil:
			sawContainer = true
			// Container is still coming up and doesn't have network data yet.
		case errors.Is(err, errContainerNotFound):
			// Container may not exist yet.
		default:
			return nil, false, err
		}

		if time.Now().After(deadline) {
			if !sawContainer {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("timed out waiting for container %s network addresses", containerName)
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func inspectContainerAddresses(containerName string) (bool, []netip.Addr, error) {
	cmd := exec.Command("docker", "inspect", containerName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := string(out)
		if strings.Contains(text, "No such object") || strings.Contains(text, "No such container") {
			return false, nil, errContainerNotFound
		}
		return false, nil, fmt.Errorf("docker inspect %s: %w: %s", containerName, err, strings.TrimSpace(text))
	}

	var payload []struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress         string `json:"IPAddress"`
				GlobalIPv6Address string `json:"GlobalIPv6Address"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return false, nil, fmt.Errorf("decode docker inspect: %w", err)
	}
	if len(payload) == 0 {
		return false, nil, fmt.Errorf("docker inspect returned empty payload for %s", containerName)
	}

	addrSet := make(map[netip.Addr]struct{})
	for _, netCfg := range payload[0].NetworkSettings.Networks {
		if ip := strings.TrimSpace(netCfg.IPAddress); ip != "" {
			if addr, err := netip.ParseAddr(ip); err == nil {
				addrSet[addr.Unmap()] = struct{}{}
			}
		}
		if ip := strings.TrimSpace(netCfg.GlobalIPv6Address); ip != "" {
			if addr, err := netip.ParseAddr(ip); err == nil {
				addrSet[addr] = struct{}{}
			}
		}
	}
	addrs := make([]netip.Addr, 0, len(addrSet))
	for addr := range addrSet {
		addrs = append(addrs, addr)
	}
	sort.Slice(addrs, func(i, j int) bool {
		return addrs[i].String() < addrs[j].String()
	})
	return payload[0].State.Running, addrs, nil
}

func installEgressRulesForAddress(addr netip.Addr, runID, mode string, allowlist resolvedAllowlist, logOut io.Writer) (egressRuleHandle, error) {
	binary, err := firewallBinary(addr)
	if err != nil {
		return egressRuleHandle{}, err
	}

	sourceCIDR := sourcePrefix(addr)
	chain := chainName(runID, addr)
	h := egressRuleHandle{
		binary: binary,
		chain:  chain,
		source: sourceCIDR,
	}
	comment := fmt.Sprintf("rascal-egress mode=%s run=%s", mode, shortHash(runID))
	if err := runFirewall(binary, "-w", "-N", chain); err != nil {
		return egressRuleHandle{}, err
	}
	if err := runFirewall(binary, "-w", "-I", "DOCKER-USER", "1", "-s", sourceCIDR, "-j", chain); err != nil {
		h.cleanup(logOut)
		return egressRuleHandle{}, err
	}

	logPrefix := fmt.Sprintf("rascal-egress[%s] ", shortHash(runID))
	appendLogReject := func(args ...string) error {
		if err := appendLogRule(binary, chain, comment, logPrefix, args...); err != nil {
			_, _ = fmt.Fprintf(logOut, "[%s] egress policy logging unavailable chain=%s error=%v\n", time.Now().UTC().Format(time.RFC3339), chain, err)
		}
		rejectArgs := []string{"-w", "-A", chain, "-m", "comment", "--comment", comment}
		rejectArgs = append(rejectArgs, args...)
		rejectArgs = append(rejectArgs, "-j", "REJECT")
		return runFirewall(binary, rejectArgs...)
	}

	switch mode {
	case egressModeSafeDefault:
		blocked := safeDefaultBlockedIPv4
		if addr.Is6() {
			blocked = safeDefaultBlockedIPv6
		}
		for _, cidr := range blocked {
			if err := appendLogReject("-d", cidr); err != nil {
				h.cleanup(logOut)
				return egressRuleHandle{}, err
			}
		}
		if err := runFirewall(binary, "-w", "-A", chain, "-j", "RETURN"); err != nil {
			h.cleanup(logOut)
			return egressRuleHandle{}, err
		}
	case egressModeAllowlist:
		allowed := allowlist.IPv4
		if addr.Is6() {
			allowed = allowlist.IPv6
		}
		for _, cidr := range allowed {
			if err := runFirewall(binary, "-w", "-A", chain, "-d", cidr, "-m", "comment", "--comment", comment, "-j", "RETURN"); err != nil {
				h.cleanup(logOut)
				return egressRuleHandle{}, err
			}
		}
		if err := appendLogReject(); err != nil {
			h.cleanup(logOut)
			return egressRuleHandle{}, err
		}
	default:
		h.cleanup(logOut)
		return egressRuleHandle{}, fmt.Errorf("unsupported egress mode %q", mode)
	}
	return h, nil
}

func appendLogRule(binary, chain, comment, prefix string, matchArgs ...string) error {
	args := []string{"-w", "-A", chain, "-m", "comment", "--comment", comment}
	args = append(args, matchArgs...)
	args = append(args, "-m", "limit", "--limit", "30/min", "--limit-burst", "20", "-j", "LOG", "--log-prefix", prefix)
	return runFirewall(binary, args...)
}

func firewallBinary(addr netip.Addr) (string, error) {
	name := "iptables"
	if addr.Is6() {
		name = "ip6tables"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s is required for runner egress policy mode", name)
	}
	return path, nil
}

func sourcePrefix(addr netip.Addr) string {
	bits := 32
	if addr.Is6() {
		bits = 128
	}
	return netip.PrefixFrom(addr, bits).String()
}

func chainName(runID string, addr netip.Addr) string {
	suffix := "4"
	if addr.Is6() {
		suffix = "6"
	}
	return fmt.Sprintf("RASCAL_EG_%s_%s", suffix, shortHash(runID+"|"+addr.String()))
}

func shortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}

func (h egressRuleHandle) cleanup(logOut io.Writer) {
	if h.binary == "" || h.chain == "" || h.source == "" {
		return
	}
	cleanupStep := func(args ...string) {
		if err := runFirewall(h.binary, args...); err != nil {
			_, _ = fmt.Fprintf(logOut, "[%s] egress cleanup warning chain=%s error=%v\n", time.Now().UTC().Format(time.RFC3339), h.chain, err)
		}
	}
	cleanupStep("-w", "-D", "DOCKER-USER", "-s", h.source, "-j", h.chain)
	cleanupStep("-w", "-F", h.chain)
	cleanupStep("-w", "-X", h.chain)
}

func runFirewall(binary string, args ...string) error {
	cmd := exec.Command(binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func resolveAllowlist(ctx context.Context, raw []string) (resolvedAllowlist, error) {
	v4 := make(map[string]struct{})
	v6 := make(map[string]struct{})

	for _, entry := range raw {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		if prefix, err := netip.ParsePrefix(entry); err == nil {
			prefix = prefix.Masked()
			if prefix.Addr().Is6() {
				v6[prefix.String()] = struct{}{}
			} else {
				v4[prefix.String()] = struct{}{}
			}
			continue
		}

		if addr, err := netip.ParseAddr(entry); err == nil {
			prefix := sourcePrefix(addr.Unmap())
			if addr.Is6() {
				prefix = sourcePrefix(addr)
				v6[prefix] = struct{}{}
			} else {
				v4[prefix] = struct{}{}
			}
			continue
		}

		host := normalizeAllowlistHost(entry)
		if host == "" {
			return resolvedAllowlist{}, fmt.Errorf("invalid allowlist entry %q", entry)
		}

		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, err := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", host)
		cancel()
		if err != nil {
			return resolvedAllowlist{}, fmt.Errorf("resolve allowlist domain %q: %w", host, err)
		}
		if len(ips) == 0 {
			return resolvedAllowlist{}, fmt.Errorf("allowlist domain %q resolved to no addresses", host)
		}
		for _, ip := range ips {
			if ip.Is6() {
				v6[sourcePrefix(ip)] = struct{}{}
			} else {
				v4[sourcePrefix(ip.Unmap())] = struct{}{}
			}
		}
	}

	out := resolvedAllowlist{
		IPv4: mapKeysSorted(v4),
		IPv6: mapKeysSorted(v6),
	}
	if len(out.IPv4) == 0 && len(out.IPv6) == 0 {
		return resolvedAllowlist{}, fmt.Errorf("allowlist is empty after parsing")
	}
	return out, nil
}

func normalizeAllowlistHost(entry string) string {
	host := strings.TrimSpace(entry)
	if host == "" {
		return ""
	}
	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err != nil {
			return ""
		}
		host = u.Hostname()
	}
	if parsed, err := netip.ParseAddr(host); err == nil {
		return parsed.String()
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSpace(host)
}

func mapKeysSorted(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
