package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/uriahlevy/hud/internal/ipc"
)

// EventHandler reacts to mutating client events. The daemon's own logic
// (debouncer, verifier runner) is wired in by `hud start`.
type EventHandler interface {
	OnWrite(file string)
	OnGoal(goal string)
}

// ErrDaemonRunning is returned by Listen when a probe of the existing
// socket file got an answer, meaning another daemon already owns the
// path. Callers can offer a "start anyway" recovery path via
// RemoveSocket + Listen.
var ErrDaemonRunning = errors.New("another hud daemon is already listening")

// RemoveSocket unlinks the socket file so a subsequent Listen can take
// over the path. Intended for the recovery flow when ErrDaemonRunning
// is returned; any process still bound to the old inode is left
// running but loses its name binding.
func RemoveSocket(sockPath string) error {
	return os.Remove(sockPath)
}

// Server hosts the Unix-socket JSON-line protocol.
type Server struct {
	state    *State
	handler  EventHandler
	listener net.Listener

	wg     sync.WaitGroup
	doneCh chan struct{}
}

// Listen creates a Unix socket at the given path, removing any stale socket
// file. The caller must close the returned Server with Close().
func Listen(sockPath string, state *State, handler EventHandler) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir socket dir: %w", err)
	}
	if err := removeStale(sockPath); err != nil {
		return nil, err
	}
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	s := &Server{
		state:    state,
		handler:  handler,
		listener: l,
		doneCh:   make(chan struct{}),
	}
	return s, nil
}

// Serve accepts connections until Close is called.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				s.wg.Wait()
				close(s.doneCh)
				return nil
			}
			return err
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.handle(c)
		}(conn)
	}
}

// Close stops accepting and waits for in-flight connections.
func (s *Server) Close() error {
	err := s.listener.Close()
	<-s.doneCh
	return err
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	var req ipc.Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeErr(conn, fmt.Errorf("bad request json: %w", err))
		return
	}
	s.state.MarkSocketActivity(req.Source == ipc.SourceMCP)
	resp := s.dispatch(req)
	_ = json.NewEncoder(conn).Encode(resp)
}

func (s *Server) dispatch(req ipc.Request) ipc.Response {
	switch req.Type {
	case ipc.TypePing:
		return okData(map[string]bool{"pong": true})
	case ipc.TypeWrite:
		var p ipc.WriteData
		if err := json.Unmarshal(req.Data, &p); err != nil {
			return errResp(err)
		}
		s.handler.OnWrite(p.File)
		return okData(struct{}{})
	case ipc.TypeGoal:
		var p ipc.GoalData
		if err := json.Unmarshal(req.Data, &p); err != nil {
			return errResp(err)
		}
		s.handler.OnGoal(p.Goal)
		return okData(struct{}{})
	case ipc.TypeStatus:
		return okData(s.state.Snapshot())
	case ipc.TypeExplain:
		var p ipc.ExplainData
		if err := json.Unmarshal(req.Data, &p); err != nil {
			return errResp(err)
		}
		v, ok := s.state.Verifier(p.Verifier)
		if !ok {
			return errResp(fmt.Errorf("verifier %q not found", p.Verifier))
		}
		return okData(v)
	default:
		return errResp(fmt.Errorf("unknown request type %q", req.Type))
	}
}

func okData(v any) ipc.Response {
	b, err := json.Marshal(v)
	if err != nil {
		return errResp(err)
	}
	return ipc.Response{OK: true, Data: b}
}

func errResp(err error) ipc.Response {
	return ipc.Response{OK: false, Error: err.Error()}
}

func writeErr(conn net.Conn, err error) {
	_ = json.NewEncoder(conn).Encode(errResp(err))
}

// removeStale deletes the socket file if it exists and isn't being served by
// a live daemon. Returns an error only if a live daemon answered the probe.
func removeStale(sockPath string) error {
	if _, err := os.Stat(sockPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	c, err := net.DialTimeout("unix", sockPath, 200*time.Millisecond)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("%w on %s", ErrDaemonRunning, sockPath)
	}
	return os.Remove(sockPath)
}
