package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/config"
	"github.com/steipete/wacli/internal/ipc"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/wa"
)

func newSendCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send messages",
	}
	cmd.AddCommand(newSendTextCmd(flags))
	cmd.AddCommand(newSendFileCmd(flags))
	return cmd
}

// resolveStoreDir returns the effective store directory for IPC client
// probing, honouring --store and $WACLI_STORE_DIR the same way newApp does.
func resolveStoreDir(flags *rootFlags) string {
	storeDir := flags.storeDir
	if storeDir == "" {
		storeDir = config.DefaultStoreDir()
	}
	return storeDir
}

func newSendTextCmd(flags *rootFlags) *cobra.Command {
	var to string
	var message string
	var noDaemon bool

	cmd := &cobra.Command{
		Use:   "text",
		Short: "Send a text message",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" || message == "" {
				return fmt.Errorf("--to and --message are required")
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			// Fast path: route through the running daemon if present.
			if !noDaemon {
				if sent, err := trySendTextViaDaemon(ctx, flags, to, message); err != nil {
					return err
				} else if sent {
					return nil
				}
			}

			// Fallback: acquire the store lock and open our own WA client.
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

			res, err := a.SendTextAndRecord(ctx, toJID, message)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent":   true,
					"to":     res.ChatJID,
					"id":     res.MsgID,
					"source": "inproc",
				})
			}
			fmt.Fprintf(os.Stdout, "Sent to %s (id %s)\n", res.ChatJID, res.MsgID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number or JID")
	cmd.Flags().StringVar(&message, "message", "", "message text")
	cmd.Flags().BoolVar(&noDaemon, "no-daemon", false, "force in-process send even if a daemon is running")
	return cmd
}

// trySendTextViaDaemon returns (true, nil) when the send succeeded via
// the daemon, (false, nil) when the daemon is not running and the caller
// should fall back to in-process, or (false, err) when the daemon was
// reachable but the send itself failed.
func trySendTextViaDaemon(ctx context.Context, flags *rootFlags, to, message string) (bool, error) {
	client := ipc.NewClient(resolveStoreDir(flags))
	if _, err := client.Ping(ctx); err != nil {
		return false, nil // no daemon; caller falls back
	}
	resp, err := client.SendText(ctx, ipc.SendTextRequest{To: to, Message: message})
	if err != nil {
		return false, err
	}
	if flags.asJSON {
		return true, out.WriteJSON(os.Stdout, map[string]any{
			"sent":   true,
			"to":     resp.ChatJID,
			"id":     resp.ID,
			"source": "daemon",
		})
	}
	fmt.Fprintf(os.Stdout, "Sent to %s (id %s)\n", resp.ChatJID, resp.ID)
	return true, nil
}
