package syncthing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxAPIResponseBytes = 4 * 1024 * 1024

func (process *Process) apiJSON(ctx context.Context, method, path string, input, output any) error {
	if process == nil || process.client == nil || process.apiKey == "" || !strings.HasPrefix(path, "/rest/") {
		return errors.New("upstream API is unavailable")
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode upstream request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://syncthing-unix"+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("X-API-Key", process.apiKey)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := process.client.Do(request)
	if err != nil {
		return fmt.Errorf("call upstream API: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxAPIResponseBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read upstream response: %w", err)
	}
	if len(payload) > maxAPIResponseBytes {
		return errors.New("upstream response exceeds limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("upstream API %s returned HTTP %d", path, response.StatusCode)
	}
	if output == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode upstream response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("upstream response contains trailing JSON")
	}
	return nil
}

func deviceConfigPath(deviceID string) string {
	return "/rest/config/devices/" + url.PathEscape(deviceID)
}
