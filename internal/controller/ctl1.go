package controller

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const ctl1MaxPayload = 1024 * 1024

// VerifyServiceStopped asks Jawaka for the authoritative CTL-1 state. A reset
// helper proceeds only after the supervisor reports both no process-group
// owner and no held/stale generation lease.
func VerifyServiceStopped(ctx context.Context, socketPath string) error {
	request := map[string]any{
		"v": 1, "id": "syncthing-reset-check", "op": "status", "service_id": ServiceID,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("query CTL-1 service state: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(2 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = connection.SetDeadline(deadline)
	if err := writeControlFrame(connection, payload); err != nil {
		return err
	}
	reply, err := readControlFrame(connection)
	if err != nil {
		return err
	}
	var response struct {
		Version         int    `json:"v"`
		ID              string `json:"id"`
		ServiceID       string `json:"service_id"`
		EffectiveState  string `json:"effective_state"`
		GenerationLease string `json:"generation_lease_state"`
		Ownership       struct {
			PGID *int `json:"pgid"`
		} `json:"ownership_identity"`
	}
	if err := json.Unmarshal(reply, &response); err != nil || response.Version != 1 ||
		response.ID != "syncthing-reset-check" || response.ServiceID != ServiceID {
		return errors.New("CTL-1 returned an invalid reset-state response")
	}
	stopped := response.EffectiveState == "stopped" || response.EffectiveState == "disabled" ||
		response.EffectiveState == "failed" || response.EffectiveState == "unavailable"
	if !stopped || response.Ownership.PGID != nil || response.GenerationLease != "none" {
		return fmt.Errorf("Syncthing service is not proven absent (state %s, lease %s)",
			response.EffectiveState, response.GenerationLease)
	}
	return nil
}

func writeControlFrame(writer io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > ctl1MaxPayload {
		return errors.New("CTL-1 reset request is outside bounds")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}

func readControlFrame(reader io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > ctl1MaxPayload {
		return nil, errors.New("CTL-1 reset response is outside bounds")
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}
