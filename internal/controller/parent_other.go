//go:build !linux

package controller

// ArmParentDeath is Linux-specific. Native development builds exercise the
// portable bootstrap logic; the production MLP1 build uses parent_linux.go.
func ArmParentDeath() error { return nil }
