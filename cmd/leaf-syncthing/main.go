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
	"time"

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
	if len(arguments) == 2 && arguments[0] == "reset-execute" {
		config, err := controller.LoadConfig()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := controller.VerifyServiceStopped(ctx, config.DaemonSocket); err != nil {
			return err
		}
		return controller.ExecuteResetPlan(config, arguments[1], controller.ResetOptions{})
	}
	if len(arguments) != 2 || arguments[0] != "service" || arguments[1] != "run" {
		return errors.New("usage: leaf-syncthing service run | doctor | reset-execute ACTION_ID")
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
	return (controller.Runner{Config: config, Logf: log.Printf}).Run(ctx)
}

func doctor() error {
	config, err := controller.LoadConfig()
	if err != nil {
		return err
	}
	resetRecovered, resetErr := controller.RecoverReset(config, controller.ResetOptions{})
	if resetErr != nil {
		return fmt.Errorf("recover destructive reset before diagnostics: %w", resetErr)
	}
	report := struct {
		Service        string `json:"service"`
		RuntimeDir     string `json:"runtime_dir"`
		UserdataPath   string `json:"userdata_path"`
		DaemonSocket   string `json:"daemon_socket"`
		Status         string `json:"status"`
		ResetRecovered bool   `json:"reset_recovered"`
	}{
		Service: controller.ServiceID, RuntimeDir: config.RuntimeDir,
		UserdataPath: config.UserdataPath, DaemonSocket: config.DaemonSocket,
		Status: "b2-controller", ResetRecovered: resetRecovered,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("encode doctor report: %w", err)
	}
	return nil
}
