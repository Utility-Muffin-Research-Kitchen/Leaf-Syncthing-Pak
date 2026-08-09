//go:build !linux

package syncthing

import (
	"errors"
	"net"
)

type RouteFiles struct {
	IPv4 string
	IPv6 string
	Sys  string
}

func DefaultRouteFiles() RouteFiles { return RouteFiles{} }

func DirectlyConnectedNetworks(RouteFiles) ([]string, error) {
	return nil, errors.New("directly connected routes require Linux")
}

func EligibleLANAddresses(RouteFiles) ([]net.IP, error) {
	return nil, errors.New("eligible LAN addresses require Linux")
}
