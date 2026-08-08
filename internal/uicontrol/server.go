package uicontrol

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/life1"
)

const (
	SocketName = "control.sock"
	ioTimeout  = 2 * time.Second
)

type Server struct {
	listener *net.UnixListener
	status   func() Status
	done     chan error
	close    sync.Once
}

// Listen creates the mode-0600 one-shot request/response socket. The framing
// intentionally reuses the already-qualified CTL-1/LIFE-1 transport.
func Listen(path string, status func() Status) (*Server, error) {
	if status == nil || !filepath.IsAbs(path) || filepath.Base(path) != SocketName {
		return nil, errors.New("UI control: absolute control.sock path and status provider are required")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("UI control: inspect socket directory: %w", err)
	}
	if parent.Mode()&os.ModeSymlink != 0 || !parent.IsDir() {
		return nil, errors.New("UI control: socket directory is not a real directory")
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}
	address, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		return nil, fmt.Errorf("UI control: resolve socket: %w", err)
	}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return nil, fmt.Errorf("UI control: listen: %w", err)
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("UI control: set socket permissions: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		_ = listener.Close()
		return nil, fmt.Errorf("UI control: socket mode/type invalid: mode=%v error=%v", fileMode(info), err)
	}

	server := &Server{listener: listener, status: status, done: make(chan error, 1)}
	go server.serve()
	return server, nil
}

func (server *Server) Done() <-chan error { return server.done }

func (server *Server) Close() error {
	if server == nil {
		return nil
	}
	var closeErr error
	server.close.Do(func() { closeErr = server.listener.Close() })
	<-server.done
	if errors.Is(closeErr, net.ErrClosed) {
		return nil
	}
	return closeErr
}

func (server *Server) serve() {
	defer close(server.done)
	for {
		connection, err := server.listener.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				server.done <- nil
			} else {
				server.done <- err
			}
			return
		}
		server.exchange(connection)
		_ = connection.Close()
	}
}

func (server *Server) exchange(connection *net.UnixConn) {
	_ = connection.SetDeadline(time.Now().Add(ioTimeout))
	payload, err := life1.Read(connection)
	if err != nil {
		return
	}
	response := Handle(payload, server.status())
	encoded, err := json.Marshal(response)
	if err != nil {
		return
	}
	if len(encoded) > life1.SemanticMaxPayload {
		encoded, _ = json.Marshal(failure(response.ID, "internal", "controller status unavailable"))
	}
	_ = life1.Write(connection, encoded)
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("UI control: inspect stale socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("UI control: socket path exists and is not a socket")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("UI control: remove stale socket: %w", err)
	}
	return nil
}

func fileMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode()
}
