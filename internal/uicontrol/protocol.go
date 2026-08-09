// Package uicontrol implements the package-private protocol between the
// resident Go controller and the foreground C/Catastrophe UI.
package uicontrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	Version             = 1
	OperationGet        = "status.get"
	OperationEnrollCard = "card.enroll"
	MaxIdentifier       = 64
)

type Request struct {
	Version   int             `json:"v"`
	ID        string          `json:"id"`
	Operation string          `json:"op"`
	Arguments json.RawMessage `json:"args"`
}

type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	Version int            `json:"v"`
	ID      string         `json:"id"`
	OK      bool           `json:"ok"`
	Result  *Status        `json:"result,omitempty"`
	Error   *ProtocolError `json:"error,omitempty"`
}

type Status struct {
	Controller   string         `json:"controller"`
	Upstream     UpstreamStatus `json:"upstream"`
	Game         GameStatus     `json:"game"`
	Recovery     RecoveryStatus `json:"recovery"`
	Cards        []CardStatus   `json:"cards"`
	Folders      []FolderStatus `json:"folders"`
	Issues       []Issue        `json:"issues"`
	Capabilities []string       `json:"capabilities"`
}

type UpstreamStatus struct {
	State    string `json:"state"`
	Version  string `json:"version"`
	DeviceID string `json:"device_id"`
}

type GameStatus struct {
	Active   bool   `json:"active"`
	LaunchID string `json:"launch_id"`
	SourceID string `json:"source_id"`
}

type RecoveryStatus struct {
	State   string `json:"state"`
	Changed bool   `json:"changed"`
}

type CardStatus struct {
	ID            string  `json:"id"`
	IDSuffix      string  `json:"id_suffix"`
	Slot          string  `json:"slot"`
	Root          string  `json:"root"`
	State         string  `json:"state"`
	Enrolled      bool    `json:"enrolled"`
	Present       bool    `json:"present"`
	Writable      bool    `json:"writable"`
	DuplicateID   bool    `json:"duplicate_id"`
	RetainedBytes int64   `json:"retained_bytes"`
	Issues        []Issue `json:"issues"`
}

type FolderStatus struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	CardID        string   `json:"card_id"`
	Kind          string   `json:"kind"`
	Path          string   `json:"path"`
	Type          string   `json:"type"`
	State         string   `json:"state"`
	Paused        bool     `json:"paused"`
	PauseReasons  []string `json:"pause_reasons"`
	PendingRescan bool     `json:"pending_rescan"`
	LocalBytes    int64    `json:"local_bytes"`
	GlobalBytes   int64    `json:"global_bytes"`
	PeerCount     int      `json:"peer_count"`
	LastSync      string   `json:"last_sync"`
	Versioning    string   `json:"versioning"`
	Issues        []Issue  `json:"issues"`
}

type Issue struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Scope     string `json:"scope"`
	SubjectID string `json:"subject_id"`
}

type Operations struct {
	Status     func() Status
	EnrollCard func(string) (Status, *ProtocolError)
}

// Handle validates one request and returns one bounded response. Request
// objects are strict; response result objects are append-only so older UIs can
// ignore fields added by later package builds.
func Handle(payload json.RawMessage, status Status) Response {
	return (Operations{Status: func() Status { return status }}).Handle(payload)
}

func (operations Operations) Handle(payload json.RawMessage) Response {
	request, err := decodeRequest(payload)
	if err != nil {
		return failure("", "bad-request", "invalid UI control request")
	}
	responseID := ""
	if validIdentifier(request.ID) {
		responseID = request.ID
	}
	if request.Version != Version {
		return failure(responseID, "unsupported-version", "unsupported UI control protocol version")
	}
	if responseID == "" {
		return failure("", "bad-request", "invalid request id")
	}
	var status Status
	switch request.Operation {
	case OperationGet:
		if !emptyObject(request.Arguments) {
			return failure(responseID, "bad-arguments", "status.get requires empty args")
		}
		if operations.Status == nil {
			return failure(responseID, "internal", "controller status unavailable")
		}
		status = operations.Status()
	case OperationEnrollCard:
		if operations.EnrollCard == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		sourceID, err := decodeEnrollCardArguments(request.Arguments)
		if err != nil {
			return failure(responseID, "bad-arguments", "card.enroll requires one valid source_id")
		}
		var operationError *ProtocolError
		status, operationError = operations.EnrollCard(sourceID)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	default:
		return failure(responseID, "unsupported-op", "unsupported UI control operation")
	}
	status.normalize()
	if err := status.validate(); err != nil {
		return failure(responseID, "internal", "controller status unavailable")
	}
	return Response{Version: Version, ID: responseID, OK: true, Result: &status}
}

func decodeEnrollCardArguments(raw json.RawMessage) (string, error) {
	var arguments struct {
		SourceID string `json:"source_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validIdentifier(arguments.SourceID) {
		return "", errors.New("invalid card enrollment arguments")
	}
	return arguments.SourceID, nil
}

func decodeRequest(payload json.RawMessage) (Request, error) {
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return Request{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Request{}, errors.New("request contains trailing JSON")
	}
	return request, nil
}

func emptyObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return false
	}
	return len(fields) == 0
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > MaxIdentifier {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' ||
			character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func failure(id, code, message string) Response {
	return Response{
		Version: Version, ID: id, OK: false,
		Error: &ProtocolError{Code: code, Message: message},
	}
}

func (status *Status) normalize() {
	if status.Cards == nil {
		status.Cards = []CardStatus{}
	} else {
		status.Cards = append([]CardStatus(nil), status.Cards...)
	}
	if status.Folders == nil {
		status.Folders = []FolderStatus{}
	} else {
		status.Folders = append([]FolderStatus(nil), status.Folders...)
	}
	if status.Issues == nil {
		status.Issues = []Issue{}
	}
	if status.Capabilities == nil {
		status.Capabilities = []string{}
	}
	for index := range status.Cards {
		if status.Cards[index].Issues == nil {
			status.Cards[index].Issues = []Issue{}
		}
	}
	for index := range status.Folders {
		if status.Folders[index].PauseReasons == nil {
			status.Folders[index].PauseReasons = []string{}
		}
		if status.Folders[index].Issues == nil {
			status.Folders[index].Issues = []Issue{}
		}
	}
}

func (status Status) validate() error {
	if !oneOf(status.Controller, "running", "recovery-pending", "error") ||
		!oneOf(status.Upstream.State, "stopped", "starting", "running", "error", "conflict") ||
		!oneOf(status.Recovery.State, "ready", "pending", "error") {
		return errors.New("invalid controller state")
	}
	if status.Upstream.State == "running" && (status.Upstream.Version == "" || status.Upstream.DeviceID == "") {
		return errors.New("incomplete running status")
	}
	if len(status.Cards) > 128 || len(status.Folders) > 128 || len(status.Issues) > 128 || len(status.Capabilities) > 64 {
		return errors.New("status exceeds row limits")
	}
	for _, card := range status.Cards {
		if card.ID == "" || !oneOf(card.State, "absent", "unenrolled", "enrolled", "invalid", "duplicate") ||
			card.RetainedBytes < 0 || len(card.Issues) > 128 {
			return errors.New("card status is outside protocol bounds")
		}
	}
	for _, folder := range status.Folders {
		if folder.LocalBytes < 0 || folder.GlobalBytes < 0 || folder.PeerCount < 0 ||
			len(folder.PauseReasons) > 16 || len(folder.Issues) > 128 {
			return errors.New("folder status is outside protocol bounds")
		}
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
