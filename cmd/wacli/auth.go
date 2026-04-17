package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
	appPkg "github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
)

func newAuthCmd(flags *rootFlags) *cobra.Command {
	var follow bool
	var idleExit time.Duration
	var downloadMedia bool
	var phone string

	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with WhatsApp (QR or phone number) and bootstrap sync",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()

			a, lk, err := newApp(ctx, flags, true, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			// Phone number pairing flow (no QR needed).
			if phone != "" {
				fmt.Fprintln(os.Stderr, "Starting phone number pairing…")
				if err := a.OpenWA(); err != nil {
					return err
				}
				code, err := a.WA().PairPhone(ctx, phone)
				if err != nil {
					return fmt.Errorf("phone pairing failed: %w", err)
				}
				fmt.Fprintf(os.Stderr, "\n========================================\n")
				fmt.Fprintf(os.Stderr, "  Pairing code: %s\n", code)
				fmt.Fprintf(os.Stderr, "========================================\n\n")
				fmt.Fprintln(os.Stderr, "On the phone, go to:")
				fmt.Fprintln(os.Stderr, "  WhatsApp → Settings → Linked Devices → Link a Device")
				fmt.Fprintln(os.Stderr, "  → \"Link with phone number instead\"")
				fmt.Fprintf(os.Stderr, "  → Enter the code: %s\n\n", code)
				fmt.Fprintln(os.Stderr, "Waiting for pairing to complete…")

				// Wait for the pairing to succeed by polling auth status.
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				timeout := time.After(5 * time.Minute)
				for {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-timeout:
						return fmt.Errorf("pairing timed out after 5 minutes")
					case <-ticker.C:
						if a.WA().IsAuthed() {
							fmt.Fprintln(os.Stderr, "Paired successfully!")
							if flags.asJSON {
								return out.WriteJSON(os.Stdout, map[string]interface{}{
									"authenticated": true,
									"method":        "phone",
								})
							}
							fmt.Fprintln(os.Stdout, "Authenticated via phone number pairing.")
							return nil
						}
					}
				}
			}

			// Standard QR code flow.
			mode := appPkg.SyncModeBootstrap
			if follow {
				mode = appPkg.SyncModeFollow
			}

			fmt.Fprintln(os.Stderr, "Starting authentication…")
			res, err := a.Sync(ctx, appPkg.SyncOptions{
				Mode:            mode,
				AllowQR:         true,
				DownloadMedia:   downloadMedia,
				RefreshContacts: true,
				RefreshGroups:   true,
				IdleExit:        idleExit,
				OnQRCode: func(code string) {
					fmt.Fprintln(os.Stderr, "\nScan this QR code with WhatsApp (Linked Devices):")
					qrterminal.GenerateHalfBlock(code, qrterminal.M, os.Stderr)
					fmt.Fprintln(os.Stderr)
				},
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]interface{}{
					"authenticated":   true,
					"messages_stored": res.MessagesStored,
				})
			}

			fmt.Fprintf(os.Stdout, "Authenticated. Messages stored: %d\n", res.MessagesStored)
			return nil
		},
	}

	cmd.Flags().BoolVar(&follow, "follow", false, "keep syncing after auth")
	cmd.Flags().DurationVar(&idleExit, "idle-exit", 30*time.Second, "exit after being idle (bootstrap/once modes)")
	cmd.Flags().BoolVar(&downloadMedia, "download-media", false, "download media in the background during sync")
	cmd.Flags().StringVar(&phone, "phone", "", "pair using phone number instead of QR (e.g. +521234567890)")

	cmd.AddCommand(newAuthStatusCmd(flags))
	cmd.AddCommand(newAuthLogoutCmd(flags))

	return cmd
}

func newAuthStatusCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show authentication status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.OpenWA(); err != nil {
				return err
			}
			authed := a.WA().IsAuthed()

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"authenticated": authed,
				})
			}
			if authed {
				fmt.Fprintln(os.Stdout, "Authenticated.")
			} else {
				fmt.Fprintln(os.Stdout, "Not authenticated. Run `wacli auth`.")
			}
			return nil
		},
	}
}

func newAuthLogoutCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Logout (invalidate session)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, true)
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
			if err := a.WA().Logout(ctx); err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"logged_out": true})
			}
			fmt.Fprintln(os.Stdout, "Logged out.")
			return nil
		},
	}
}
