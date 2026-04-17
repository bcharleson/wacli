// Package ipc defines the local Unix-socket HTTP protocol used by the
// `wacli daemon` to serve send/status requests from short-lived CLI
// processes. Keeping this in its own package lets client (send commands)
// and server (daemon) share types without dragging the app package into
// the CLI binary twice.
package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

// SocketFilename is the socket file name placed inside the store dir.
const SocketFilename = "daemon.sock"

// SocketPath returns the absolute path to the daemon socket inside the
// given store directory.
func SocketPath(storeDir string) string {
	return filepath.Join(storeDir, SocketFilename)
}

// StatusResponse is returned by GET /status.
type StatusResponse struct {
	Authenticated bool   `json:"authenticated"`
	Connected     bool   `json:"connected"`
	Version       string `json:"version"`
}

// SendTextRequest is the body of POST /send/text.
type SendTextRequest struct {
	To      string `json:"to"`
	Message string `json:"message"`
}

// SendTextResponse is the body of a successful POST /send/text.
type SendTextResponse struct {
	ID       string `json:"id"`
	ChatJID  string `json:"chat_jid"`
	ChatName string `json:"chat_name"`
}

// SendFileRequest is the body of POST /send/file.
// Path must be an absolute path on the daemon's filesystem — the socket is
// local, so there is no need to stream bytes over it.
type SendFileRequest struct {
	To       string `json:"to"`
	Path     string `json:"path"`
	Filename string `json:"filename,omitempty"`
	Caption  string `json:"caption,omitempty"`
	Mime     string `json:"mime,omitempty"`
}

// SendFileResponse is the body of a successful POST /send/file.
type SendFileResponse struct {
	ID        string `json:"id"`
	ChatJID   string `json:"chat_jid"`
	ChatName  string `json:"chat_name"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mime_type"`
	MediaType string `json:"media"`
}

// ErrorResponse is the body of any non-2xx response.
type ErrorResponse struct {
	Error string `json:"error"`
}

// Client speaks HTTP over a Unix socket to the daemon.
type Client struct {
	http       *http.Client
	socketPath string
}

// NewClient builds a client that targets $storeDir/daemon.sock.
func NewClient(storeDir string) *Client {
	sock := SocketPath(storeDir)
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 500 * time.Millisecond}
			return d.DialContext(ctx, "unix", sock)
		},
	}
	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   0, // rely on ctx — upload can take a while
		},
		socketPath: sock,
	}
}

// SocketPath returns the socket path the client will dial.
func (c *Client) SocketPath() string { return c.socketPath }

// Ping checks whether a daemon is running and responsive. It uses a short
// per-call deadline so callers can fail over to in-process mode quickly.
func (c *Client) Ping(ctx context.Context) (StatusResponse, error) {
	pctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var resp StatusResponse
	if err := c.do(pctx, http.MethodGet, "/status", nil, &resp); err != nil {
		return StatusResponse{}, err
	}
	return resp, nil
}

// SendText sends a text message via the daemon.
func (c *Client) SendText(ctx context.Context, req SendTextRequest) (SendTextResponse, error) {
	var resp SendTextResponse
	if err := c.do(ctx, http.MethodPost, "/send/text", req, &resp); err != nil {
		return SendTextResponse{}, err
	}
	return resp, nil
}

// SendFile sends a file via the daemon. The file is read from the daemon's
// filesystem at req.Path.
func (c *Client) SendFile(ctx context.Context, req SendFileRequest) (SendFileResponse, error) {
	var resp SendFileResponse
	if err := c.do(ctx, http.MethodPost, "/send/file", req, &resp); err != nil {
		return SendFileResponse{}, err
	}
	return resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	// The Host header is ignored when dialing a unix socket but required.
	req, err := http.NewRequestWithContext(ctx, method, "http://wacli"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode >= 400 {
		var errResp ErrorResponse
		if err := json.Unmarshal(data, &errResp); err == nil && errResp.Error != "" {
			return fmt.Errorf("daemon: %s", errResp.Error)
		}
		return fmt.Errorf("daemon returned HTTP %d", res.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
