// Command leaf-syncthing is the pure-Go resident controller and diagnostic
// entrypoint. The foreground Catastrophe UI is a separate C executable.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/controller"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("leaf-syncthing: %v", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 1 && arguments[0] == "doctor" {
		return doctor()
	}
	if len(arguments) != 2 || arguments[0] != "service" || arguments[1] != "run" {
		return errors.New("usage: leaf-syncthing service run | doctor")
	}
	if err := controller.ArmParentDeath(); err != nil {
		return err
	}
	if err := controller.ValidateAndGuardLease(os.Getenv); err != nil {
		return err
	}
	config, err := controller.LoadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	session, err := (controller.Runner{Config: config, Logf: log.Printf}).Bootstrap(ctx)
	if err != nil {
		if errors.Is(err, controller.ErrLifecycleStop) {
			return nil
		}
		return err
	}
	defer session.Close()

	// The package launcher still rejects this B1 development artifact. Keeping
	// the fail-closed boundary here prevents an upstream process from appearing
	// before config recovery/generation and offline pause editing are complete.
	return controller.ErrB1Incomplete
}

func doctor() error {
	config, err := controller.LoadConfig()
	if err != nil {
		return err
	}
	report := struct {
		Service      string `json:"service"`
		RuntimeDir   string `json:"runtime_dir"`
		UserdataPath string `json:"userdata_path"`
		DaemonSocket string `json:"daemon_socket"`
		Status       string `json:"status"`
	}{
		Service: controller.ServiceID, RuntimeDir: config.RuntimeDir,
		UserdataPath: config.UserdataPath, DaemonSocket: config.DaemonSocket,
		Status: "b1-bootstrap",
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("encode doctor report: %w", err)
	}
	return nil
}
