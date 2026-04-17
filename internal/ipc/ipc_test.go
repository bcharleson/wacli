package ipc

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// shortTempDir returns a short-path temp dir suitable for Unix sockets on
// macOS, which caps sun_path at ~104 bytes. t.TempDir() lives under
// /var/folders/... and routinely exceeds that limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wacli-ipc-")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeHandler is a minimal Handler implementation for round-trip tests.
type fakeHandler struct {
	mu          sync.Mutex
	statusCalls int
	lastText    SendTextRequest
	lastFile    SendFileRequest
	sendTextErr error
	sendFileErr error
}

func (f *fakeHandler) Status(_ context.Context) (StatusResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	return StatusResponse{Authenticated: true, Connected: true, Version: "test"}, nil
}

func (f *fakeHandler) SendText(_ context.Context, req SendTextRequest) (SendTextResponse, error) {
	f.mu.Lock()
	f.lastText = req
	err := f.sendTextErr
	f.mu.Unlock()
	if err != nil {
		return SendTextResponse{}, err
	}
	return SendTextResponse{ID: "MSG123", ChatJID: req.To + "@s.whatsapp.net", ChatName: "Test"}, nil
}

func (f *fakeHandler) SendFile(_ context.Context, req SendFileRequest) (SendFileResponse, error) {
	f.mu.Lock()
	f.lastFile = req
	err := f.sendFileErr
	f.mu.Unlock()
	if err != nil {
		return SendFileResponse{}, err
	}
	return SendFileResponse{
		ID:        "FILE123",
		ChatJID:   req.To + "@s.whatsapp.net",
		ChatName:  "Test",
		Filename:  req.Filename,
		MimeType:  req.Mime,
		MediaType: "document",
	}, nil
}

func startTestServer(t *testing.T) (*Client, *fakeHandler, func()) {
	t.Helper()
	dir := shortTempDir(t)
	sock := filepath.Join(dir, SocketFilename)
	h := &fakeHandler{}
	srv := NewServer(h)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx, sock)
		close(done)
	}()

	// Wait briefly for the listener to come up.
	deadline := time.Now().Add(2 * time.Second)
	client := NewClient(dir)
	for time.Now().Before(deadline) {
		pctx, pcancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, err := client.Ping(pctx)
		pcancel()
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cleanup := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("server did not shut down")
		}
	}
	return client, h, cleanup
}

func TestClientServerRoundtripStatus(t *testing.T) {
	client, h, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	resp, err := client.Ping(ctx)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if !resp.Authenticated || !resp.Connected || resp.Version != "test" {
		t.Fatalf("unexpected status: %+v", resp)
	}
	if h.statusCalls == 0 {
		t.Fatal("handler Status not invoked")
	}
}

func TestClientServerRoundtripSendText(t *testing.T) {
	client, h, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	resp, err := client.SendText(ctx, SendTextRequest{To: "5215551112222", Message: "hello"})
	if err != nil {
		t.Fatalf("send text: %v", err)
	}
	if resp.ID != "MSG123" {
		t.Fatalf("unexpected id: %q", resp.ID)
	}
	if h.lastText.To != "5215551112222" || h.lastText.Message != "hello" {
		t.Fatalf("handler saw unexpected req: %+v", h.lastText)
	}
}

func TestClientServerRoundtripSendFile(t *testing.T) {
	client, h, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	req := SendFileRequest{
		To:       "5215551112222",
		Path:     "/tmp/example.pdf",
		Filename: "example.pdf",
		Caption:  "hi",
		Mime:     "application/pdf",
	}
	resp, err := client.SendFile(ctx, req)
	if err != nil {
		t.Fatalf("send file: %v", err)
	}
	if resp.ID != "FILE123" || resp.Filename != "example.pdf" {
		t.Fatalf("unexpected resp: %+v", resp)
	}
	if h.lastFile.Path != "/tmp/example.pdf" {
		t.Fatalf("handler saw unexpected req: %+v", h.lastFile)
	}
}

func TestSendTextRequiredFieldsRejected(t *testing.T) {
	client, _, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	if _, err := client.SendText(ctx, SendTextRequest{To: "", Message: "hi"}); err == nil {
		t.Fatal("expected error for missing To")
	}
}

func TestPingMissingSocketFailsFast(t *testing.T) {
	dir := shortTempDir(t) // no server
	client := NewClient(dir)

	start := time.Now()
	_, err := client.Ping(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error when no daemon is running")
	}
	// Must return well under the 500ms ceiling when the socket file is absent.
	if elapsed > 400*time.Millisecond {
		t.Fatalf("ping too slow for missing socket: %s", elapsed)
	}
}

func TestSocketPathJoinsStoreDir(t *testing.T) {
	got := SocketPath("/var/lib/wacli")
	want := "/var/lib/wacli/" + SocketFilename
	if got != want {
		t.Fatalf("SocketPath = %q, want %q", got, want)
	}
}
