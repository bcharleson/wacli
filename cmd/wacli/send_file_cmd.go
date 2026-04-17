package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/ipc"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/wa"
)

func newSendFileCmd(flags *rootFlags) *cobra.Command {
	var to string
	var filePath string
	var filename string
	var caption string
	var mimeOverride string
	var noDaemon bool

	cmd := &cobra.Command{
		Use:   "file",
		Short: "Send a file (image/video/audio/document)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" || filePath == "" {
				return fmt.Errorf("--to and --file are required")
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			if !noDaemon {
				if sent, err := trySendFileViaDaemon(ctx, flags, to, filePath, filename, caption, mimeOverride); err != nil {
					return err
				} else if sent {
					return nil
				}
			}

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			toJID, err := wa.ParseUserOrJID(to)
			if err != nil {
				return err
			}

			res, err := a.SendFileAndRecord(ctx, toJID, filePath, filename, caption, mimeOverride)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent":   true,
					"to":     res.ChatJID,
					"id":     res.MsgID,
					"source": "inproc",
					"file": map[string]string{
						"name":      res.Filename,
						"mime_type": res.MimeType,
						"media":     res.MediaType,
					},
				})
			}
			fmt.Fprintf(os.Stdout, "Sent %s to %s (id %s)\n", res.Filename, res.ChatJID, res.MsgID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number or JID")
	cmd.Flags().StringVar(&filePath, "file", "", "path to file")
	cmd.Flags().StringVar(&filename, "filename", "", "display name for the file (defaults to basename of --file)")
	cmd.Flags().StringVar(&caption, "caption", "", "caption (images/videos/documents)")
	cmd.Flags().StringVar(&mimeOverride, "mime", "", "override detected mime type")
	cmd.Flags().BoolVar(&noDaemon, "no-daemon", false, "force in-process send even if a daemon is running")
	return cmd
}

func trySendFileViaDaemon(ctx context.Context, flags *rootFlags, to, filePath, filename, caption, mimeOverride string) (bool, error) {
	client := ipc.NewClient(resolveStoreDir(flags))
	if _, err := client.Ping(ctx); err != nil {
		return false, nil
	}
	// The daemon reads the file from disk itself; it must therefore be an
	// absolute path that is accessible to the daemon process.
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return false, fmt.Errorf("resolve file path: %w", err)
	}
	resp, err := client.SendFile(ctx, ipc.SendFileRequest{
		To:       to,
		Path:     abs,
		Filename: filename,
		Caption:  caption,
		Mime:     mimeOverride,
	})
	if err != nil {
		return false, err
	}
	if flags.asJSON {
		return true, out.WriteJSON(os.Stdout, map[string]any{
			"sent":   true,
			"to":     resp.ChatJID,
			"id":     resp.ID,
			"source": "daemon",
			"file": map[string]string{
				"name":      resp.Filename,
				"mime_type": resp.MimeType,
				"media":     resp.MediaType,
			},
		})
	}
	fmt.Fprintf(os.Stdout, "Sent %s to %s (id %s)\n", resp.Filename, resp.ChatJID, resp.ID)
	return true, nil
}
