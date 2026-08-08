//go:build linux

package syncthing

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type RouteFiles struct {
	IPv4 string
	IPv6 string
	Sys  string
}

func DefaultRouteFiles() RouteFiles {
	return RouteFiles{IPv4: "/proc/net/route", IPv6: "/proc/net/ipv6_route", Sys: "/sys/class/net"}
}

func DirectlyConnectedNetworks(files RouteFiles) ([]string, error) {
	if files.IPv4 == "" || files.IPv6 == "" || files.Sys == "" {
		return nil, errors.New("route sources are incomplete")
	}
	networks := make(map[string]struct{})
	if err := readIPv4Routes(files, networks); err != nil {
		return nil, err
	}
	if err := readIPv6Routes(files, networks); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	result := make([]string, 0, len(networks))
	for network := range networks {
		result = append(result, network)
	}
	sort.Strings(result)
	return result, nil
}

func eligibleInterface(root, name string) bool {
	if name == "" || name == "lo" || strings.HasPrefix(name, "tun") ||
		strings.HasPrefix(name, "tap") || strings.HasPrefix(name, "wg") ||
		strings.HasPrefix(name, "ppp") || strings.HasPrefix(name, "veth") ||
		strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") ||
		strings.HasPrefix(name, "tailscale") {
		return false
	}
	contents, err := os.ReadFile(filepath.Join(root, name, "type"))
	return err == nil && strings.TrimSpace(string(contents)) == "1"
}

func readIPv4Routes(files RouteFiles, networks map[string]struct{}) error {
	file, err := os.Open(files.IPv4)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || !eligibleInterface(files.Sys, fields[0]) || fields[2] != "00000000" {
			continue
		}
		destination, err1 := strconv.ParseUint(fields[1], 16, 32)
		mask, err2 := strconv.ParseUint(fields[7], 16, 32)
		if err1 != nil || err2 != nil || mask == 0 {
			continue
		}
		destinationBytes := make(net.IP, 4)
		maskBytes := make(net.IPMask, 4)
		binary.LittleEndian.PutUint32(destinationBytes, uint32(destination))
		binary.LittleEndian.PutUint32(maskBytes, uint32(mask))
		ones, bits := maskBytes.Size()
		if bits != 32 || ones <= 0 || ones >= 32 {
			continue
		}
		network := (&net.IPNet{IP: destinationBytes.Mask(maskBytes), Mask: maskBytes}).String()
		networks[network] = struct{}{}
	}
	return scanner.Err()
}

func readIPv6Routes(files RouteFiles, networks map[string]struct{}) error {
	file, err := os.Open(files.IPv6)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || !eligibleInterface(files.Sys, fields[9]) ||
			fields[4] != strings.Repeat("0", 32) {
			continue
		}
		prefix, err := strconv.ParseUint(fields[1], 16, 8)
		decoded, decodeErr := hex.DecodeString(fields[0])
		if err != nil || decodeErr != nil || len(decoded) != net.IPv6len || prefix == 0 || prefix >= 128 {
			continue
		}
		mask := net.CIDRMask(int(prefix), 128)
		networks[(&net.IPNet{IP: net.IP(decoded).Mask(mask), Mask: mask}).String()] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read IPv6 routes: %w", err)
	}
	return nil
}
