package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"time"
)

const (
	dockerHostTunInterface      = "mihomo"
	dockerHostTunCommandTimeout = 8 * time.Second
)

var (
	dockerHostTunRouteWait  = 5 * time.Second
	dockerHostTunRouteRetry = 250 * time.Millisecond
	dockerHostTunCommand    = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
)

func (a *App) afterServiceStart(name string) {
	if normalizeServiceName(strings.ToLower(strings.TrimSpace(name))) != "mihomo" {
		return
	}
	go a.applyDockerHostTunRouteFix()
}

func (a *App) applyDockerHostTunRouteFix() {
	a.applyDockerHostTunRouteFixWithWait(dockerHostTunRouteWait)
}

func (a *App) applyDockerHostTunRouteFixWithWait(wait time.Duration) {
	if !a.shouldApplyDockerHostTunRouteFix() {
		return
	}
	cfg, ok := a.latestSetupConfig()
	if !ok {
		return
	}
	cfg.defaults()
	cidr := fakeIPv4RouteCIDR(cfg.FakeIPRangeV4)
	src, ok := fakeIPv4RouteSource(cidr)
	if !ok {
		log.Printf("warning: docker host-tun route fix skipped: invalid fake-ip IPv4 range %q", cfg.FakeIPRangeV4)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), dockerHostTunCommandTimeout+wait)
	defer cancel()

	if !waitForDockerHostTunInterface(ctx, wait) {
		log.Printf("warning: docker host-tun route fix skipped: interface %s is not ready; see docs/docker.md for manual fallback", dockerHostTunInterface)
		return
	}
	if out, err := dockerHostTunCommand(ctx, "ip", "route", "replace", cidr, "dev", dockerHostTunInterface, "src", src); err != nil {
		log.Printf("warning: docker host-tun route fix failed to replace FakeIP route: %v: %s", err, strings.TrimSpace(string(out)))
		return
	}

	iface, err := dockerHostTunDefaultInterface(ctx)
	if err != nil {
		log.Printf("warning: docker host-tun route fix could not detect default interface for rp_filter: %v", err)
		return
	}
	if iface == "" {
		log.Print("warning: docker host-tun route fix skipped rp_filter update: default interface not found")
		return
	}
	path := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/rp_filter", iface)
	if out, err := dockerHostTunCommand(ctx, "sh", "-c", `printf 0 > "$1"`, "sh", path); err != nil {
		log.Printf("warning: docker host-tun route fix could not disable rp_filter on %s: %v: %s", iface, err, strings.TrimSpace(string(out)))
	}
}

func (a *App) shouldApplyDockerHostTunRouteFix() bool {
	if !IsDockerRuntime() || DockerNetworkMode() != "host-tun" {
		return false
	}
	cfg, ok := a.latestSetupConfig()
	if !ok {
		return false
	}
	cfg.defaults()
	return strings.EqualFold(cfg.ProxyCore, "mihomo") && isTUNProxyMode(cfg.LinuxProxyMode)
}

func waitForDockerHostTunInterface(ctx context.Context, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if _, err := dockerHostTunCommand(ctx, "ip", "link", "show", dockerHostTunInterface); err == nil {
			return true
		}
		if wait <= 0 || time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(dockerHostTunRouteRetry):
		}
	}
}

func dockerHostTunDefaultInterface(ctx context.Context) (string, error) {
	out, err := dockerHostTunCommand(ctx, "ip", "-4", "route", "show", "default")
	if err != nil {
		return "", err
	}
	return parseDockerHostTunDefaultInterface(string(out)), nil
}

func parseDockerHostTunDefaultInterface(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field == "dev" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}

func fakeIPv4RouteSource(cidr string) (string, bool) {
	_, ipNet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return "", false
	}
	ip := ipNet.IP.To4()
	if ip == nil {
		return "", false
	}
	src := append(net.IP(nil), ip...)
	for i := len(src) - 1; i >= 0; i-- {
		src[i]++
		if src[i] != 0 {
			return src.String(), true
		}
	}
	return "", false
}
