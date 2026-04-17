package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestPhoneVariants(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "mexico e164 adds jid form",
			in:   "525562237227",
			want: []string{"525562237227", "5215562237227"},
		},
		{
			name: "mexico jid adds e164 form",
			in:   "5215562237227",
			want: []string{"5215562237227", "525562237227"},
		},
		{
			name: "brazil old form adds nine",
			in:   "551187654321", // 55 + 11 + 87654321 = 12 digits, 8-digit local
			want: []string{"551187654321", "5511987654321"},
		},
		{
			name: "brazil new form adds stripped nine",
			in:   "5511987654321", // 55 + 11 + 987654321 = 13 digits
			want: []string{"5511987654321", "551187654321"},
		},
		{
			name: "argentina adds 549 form",
			in:   "541123456789", // 54 + 11 + 23456789 = 12 digits
			want: []string{"541123456789", "5491123456789"},
		},
		{
			name: "argentina strips 549",
			in:   "5491123456789",
			want: []string{"5491123456789", "541123456789"},
		},
		{
			name: "us number unchanged",
			in:   "14155551234",
			want: []string{"14155551234"},
		},
		{
			name: "spain number unchanged",
			in:   "34612345678",
			want: []string{"34612345678"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := phoneVariants(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("phoneVariants(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractDigits(t *testing.T) {
	if got := extractDigits("+52 (55) 6223-7227"); got != "525562237227" {
		t.Fatalf("unexpected: %q", got)
	}
	if got := extractDigits("no digits"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestResolveRecipientJIDPassthrough(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA() // should not be called for JID path

	jid, err := a.ResolveRecipient(context.Background(), "5215562237227@s.whatsapp.net")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if jid.String() != "5215562237227@s.whatsapp.net" {
		t.Fatalf("got %s", jid.String())
	}
}

func TestResolveRecipientMexicoE164PicksJIDForm(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	// Simulate real WhatsApp behaviour: only the "521..." variant is registered.
	f.isOnWhatsAppFn = func(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		out := make([]types.IsOnWhatsAppResponse, 0, len(phones))
		for _, p := range phones {
			registered := strings.HasPrefix(p, "+5215")
			user := strings.TrimPrefix(p, "+")
			out = append(out, types.IsOnWhatsAppResponse{
				Query: p,
				IsIn:  registered,
				JID:   types.JID{User: user, Server: types.DefaultUserServer},
			})
		}
		return out, nil
	}

	jid, err := a.ResolveRecipient(context.Background(), "+525562237227")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if jid.User != "5215562237227" {
		t.Fatalf("expected JID user=5215562237227, got %s", jid.User)
	}
}

func TestResolveRecipientNoneRegisteredReturnsClearError(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	f.isOnWhatsAppFn = func(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		out := make([]types.IsOnWhatsAppResponse, 0, len(phones))
		for _, p := range phones {
			out = append(out, types.IsOnWhatsAppResponse{Query: p, IsIn: false})
		}
		return out, nil
	}

	_, err := a.ResolveRecipient(context.Background(), "+19999999999")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("error should mention 'not registered': %v", err)
	}
	if !strings.Contains(err.Error(), "pass the JID directly") {
		t.Fatalf("error should suggest passing JID directly: %v", err)
	}
}

func TestResolveRecipientLookupErrorSurfaced(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	wantErr := errors.New("info query timed out")
	f.isOnWhatsAppFn = func(_ context.Context, _ []string) ([]types.IsOnWhatsAppResponse, error) {
		return nil, wantErr
	}

	_, err := a.ResolveRecipient(context.Background(), "+525562237227")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped timeout, got %v", err)
	}
	if !strings.Contains(err.Error(), "user@s.whatsapp.net") {
		t.Fatalf("error should recommend passing JID directly, got %v", err)
	}
}

func TestResolveRecipientRejectsJunk(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()

	if _, err := a.ResolveRecipient(context.Background(), "   "); err == nil {
		t.Fatal("expected error for blank input")
	}
	if _, err := a.ResolveRecipient(context.Background(), "no digits here"); err == nil {
		t.Fatal("expected error for non-numeric input")
	}
}
