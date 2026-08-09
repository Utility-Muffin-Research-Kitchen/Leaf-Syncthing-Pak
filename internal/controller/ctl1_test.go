package controller

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVerifyServiceStoppedUsesAuthoritativeStatus(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
		wantErr  bool
	}{
		{
			name: "absent",
			response: `{"v":1,"id":"syncthing-reset-check","service_id":"org.umrk.syncthing",` +
				`"effective_state":"disabled","ownership_identity":{"pgid":null},"generation_lease_state":"none"}`,
		},
		{
			name: "owned",
			response: `{"v":1,"id":"syncthing-reset-check","service_id":"org.umrk.syncthing",` +
				`"effective_state":"stopped","ownership_identity":{"pgid":123},"generation_lease_state":"held"}`,
			wantErr: true,
		},
		{
			name: "list-row-is-insufficient",
			response: `{"v":1,"id":"syncthing-reset-check","service_id":"org.umrk.syncthing",` +
				`"effective_state":"disabled"}`,
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory, err := os.MkdirTemp("/tmp", "leaf-ctl1-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(directory) })
			socketPath := filepath.Join(directory, "jawakad.sock")
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverDone := make(chan error, 1)
			go func() {
				connection, err := listener.Accept()
				if err != nil {
					serverDone <- err
					return
				}
				defer connection.Close()
				payload, err := readControlFrame(connection)
				if err == nil {
					var request struct {
						Operation string `json:"op"`
						ServiceID string `json:"service_id"`
					}
					err = json.Unmarshal(payload, &request)
					if err == nil && (request.Operation != "status" || request.ServiceID != ServiceID) {
						err = context.Canceled
					}
				}
				if err == nil {
					err = writeControlFrame(connection, []byte(test.response))
				}
				serverDone <- err
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err = VerifyServiceStopped(ctx, socketPath)
			if (err != nil) != test.wantErr {
				t.Fatalf("VerifyServiceStopped() error = %v, want error %v", err, test.wantErr)
			}
			if serverErr := <-serverDone; serverErr != nil {
				t.Fatal(serverErr)
			}
		})
	}
}
