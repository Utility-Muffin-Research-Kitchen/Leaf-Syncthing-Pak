//go:build !linux

package syncthing

import "errors"

type RouteFiles struct {
	IPv4 string
	IPv6 string
	Sys  string
}

func DefaultRouteFiles() RouteFiles { return RouteFiles{} }

func DirectlyConnectedNetworks(RouteFiles) ([]string, error) {
	return nil, errors.New("directly connected routes require Linux")
}
