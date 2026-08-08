//go:build linux

package syncthing

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDirectlyConnectedNetworksUsesPhysicalOnLinkRoutes(t *testing.T) {
	directory := t.TempDir()
	sys := filepath.Join(directory, "sys")
	for _, name := range []string{"eth0", "tun0"} {
		if err := os.MkdirAll(filepath.Join(sys, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sys, name, "type"), []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ipv4 := filepath.Join(directory, "route")
	ipv4Contents := "Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\n" +
		"eth0 00000000 0101A8C0 0003 0 0 100 00000000 0 0 0\n" +
		"eth0 0004A8C0 00000000 0001 0 0 100 00FFFFFF 0 0 0\n" +
		"tun0 0000080A 00000000 0001 0 0 10 0000FFFF 0 0 0\n"
	if err := os.WriteFile(ipv4, []byte(ipv4Contents), 0o644); err != nil {
		t.Fatal(err)
	}
	ipv6 := filepath.Join(directory, "ipv6_route")
	ipv6Contents := "20010db8000000000000000000000000 40 0000000000000000000000000000 00 0000000000000000000000000000 00000064 00000000 00000000 00000001 eth0\n" +
		"fd000000000000000000000000000000 40 0000000000000000000000000000 00 0000000000000000000000000000 0000000a 00000000 00000000 00000001 tun0\n"
	if err := os.WriteFile(ipv6, []byte(ipv6Contents), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DirectlyConnectedNetworks(RouteFiles{IPv4: ipv4, IPv6: ipv6, Sys: sys})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.4.0/24", "2001:db8::/64"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("networks = %v, want %v", got, want)
	}
}
