package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Handler is implemented by anything that can fulfil IPC requests. The
// daemon wires an *app.App-backed adapter to this interface so the ipc
// package stays free of whatsmeow/app dependencies (and thus testable in
// isolation).
type Handler interface {
	Status(ctx context.Context) (StatusResponse, error)
	SendText(ctx context.Context, req SendTextRequest) (SendTextResponse, error)
	SendFile(ctx context.Context, req SendFileRequest) (SendFileResponse, error)
}

// Server exposes the Handler over HTTP on a Unix socket.
// Sends are serialized with a mutex because whatsmeow's upload+send path
// is not designed for concurrent callers; status is unaffected.
type Server struct {
	h        Handler
	sendMu   sync.Mutex
	listener net.Listener
	srv      *http.Server
	sockPath string
}

// NewServer creates a server that will call h for requests.
func NewServer(h Handler) *Server {
	return &Server{h: h}
}

// Serve binds the Unix socket at socketPath and serves requests until ctx
// is cancelled. It returns nil on clean shutdown and a non-nil error on
// bind failure or other fatal issues.
//
// A stale socket file (left behind by a crashed daemon) is removed before
// binding. The socket permissions are tightened to 0600 after bind so
// only the owning user can talk to the daemon.
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	if strings.TrimSpace(socketPath) == "" {
		return fmt.Errorf("socket path is required")
	}
	// Best-effort: remove stale socket from a previous run.
	if info, err := os.Stat(socketPath); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(socketPath)
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(socketPath)
		return fmt.Errorf("chmod socket: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/send/text", s.handleSendText)
	mux.HandleFunc("/send/file", s.handleSendFile)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// No overall timeout — a media upload can legitimately take longer
		// than any reasonable HTTP timeout.
	}

	s.listener = ln
	s.srv = srv
	s.sockPath = socketPath

	serveErr := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			serveErr <- nil
			return
		}
		serveErr <- err
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		_ = os.Remove(socketPath)
		return nil
	case err := <-serveErr:
		_ = os.Remove(socketPath)
		return err
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	resp, err := s.h.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSendText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req SendTextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if strings.TrimSpace(req.To) == "" || strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, "to and message are required")
		return
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	resp, err := s.h.SendText(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSendFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req SendFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if strings.TrimSpace(req.To) == "" || strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, "to and path are required")
		return
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	resp, err := s.h.SendFile(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
