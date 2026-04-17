package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	appPkg "github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/config"
	"github.com/steipete/wacli/internal/ipc"
	"github.com/steipete/wacli/internal/wa"
)

// appHandler adapts *app.App to the ipc.Handler interface. It is kept as
// a small local adapter (rather than a method set on *app.App) so the ipc
// package does not need to import the app package.
type appHandler struct {
	a *appPkg.App
}

func (h *appHandler) Status(ctx context.Context) (ipc.StatusResponse, error) {
	return ipc.StatusResponse{
		Authenticated: h.a.WA() != nil && h.a.WA().IsAuthed(),
		Connected:     h.a.WA() != nil && h.a.WA().IsConnected(),
		Version:       h.a.Version(),
	}, nil
}

func (h *appHandler) SendText(ctx context.Context, req ipc.SendTextRequest) (ipc.SendTextResponse, error) {
	toJID, err := wa.ParseUserOrJID(req.To)
	if err != nil {
		return ipc.SendTextResponse{}, err
	}
	res, err := h.a.SendTextAndRecord(ctx, toJID, req.Message)
	if err != nil {
		return ipc.SendTextResponse{}, err
	}
	return ipc.SendTextResponse{
		ID:       res.MsgID,
		ChatJID:  res.ChatJID,
		ChatName: res.ChatName,
	}, nil
}

func (h *appHandler) SendFile(ctx context.Context, req ipc.SendFileRequest) (ipc.SendFileResponse, error) {
	toJID, err := wa.ParseUserOrJID(req.To)
	if err != nil {
		return ipc.SendFileResponse{}, err
	}
	// Enforce absolute path: the daemon has no working-dir context for
	// the calling process, so relative paths would be ambiguous.
	if !filepath.IsAbs(req.Path) {
		return ipc.SendFileResponse{}, fmt.Errorf("path must be absolute (got %q)", req.Path)
	}
	res, err := h.a.SendFileAndRecord(ctx, toJID, req.Path, req.Filename, req.Caption, req.Mime)
	if err != nil {
		return ipc.SendFileResponse{}, err
	}
	return ipc.SendFileResponse{
		ID:        res.MsgID,
		ChatJID:   res.ChatJID,
		ChatName:  res.ChatName,
		Filename:  res.Filename,
		MimeType:  res.MimeType,
		MediaType: res.MediaType,
	}, nil
}

func newDaemonCmd(flags *rootFlags) *cobra.Command {
	var downloadMedia bool
	var refreshContacts bool
	var refreshGroups bool
	var maxReconnect time.Duration

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the sync loop and serve send/status requests on a Unix socket",
		Long: `Run the WhatsApp sync loop in the foreground and expose a local
HTTP API on $WACLI_STORE_DIR/daemon.sock so one-shot commands like
"wacli send" can be invoked without fighting for the store lock.

The daemon holds the store lock for its entire lifetime. Send commands
probe the socket and fall back to in-process mode when the daemon is
not running, so existing workflows keep working.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}

			storeDir := flags.storeDir
			if storeDir == "" {
				storeDir = config.DefaultStoreDir()
			}
			sockPath := ipc.SocketPath(storeDir)

			srv := ipc.NewServer(&appHandler{a: a})
			srvErrCh := make(chan error, 1)
			go func() {
				srvErrCh <- srv.Serve(ctx, sockPath)
			}()

			fmt.Fprintf(os.Stderr, "wacli daemon listening on %s\n", sockPath)

			res, err := a.Sync(ctx, appPkg.SyncOptions{
				Mode:            appPkg.SyncModeFollow,
				AllowQR:         false,
				DownloadMedia:   downloadMedia,
				RefreshContacts: refreshContacts,
				RefreshGroups:   refreshGroups,
				MaxReconnect:    maxReconnect,
			})

			// Sync has returned (ctx cancelled or fatal error); stop the
			// IPC server and wait for it to finish cleaning up the socket.
			stop()
			if serveErr := <-srvErrCh; serveErr != nil {
				fmt.Fprintf(os.Stderr, "ipc server: %v\n", serveErr)
			}

			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Daemon stopped. Messages stored this session: %d\n", res.MessagesStored)
			return nil
		},
	}

	cmd.Flags().BoolVar(&downloadMedia, "download-media", false, "download media in the background during sync")
	cmd.Flags().BoolVar(&refreshContacts, "refresh-contacts", true, "refresh contacts from session store into local DB at startup")
	cmd.Flags().BoolVar(&refreshGroups, "refresh-groups", true, "refresh joined groups into local DB at startup")
	cmd.Flags().DurationVar(&maxReconnect, "max-reconnect", 5*time.Minute, "give up reconnecting after this duration (0 = unlimited)")
	return cmd
}
