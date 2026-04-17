package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// ResolveTimeout is the per-call deadline for turning a phone number
// into a canonical JID via `IsOnWhatsApp`. Must be short: if the first
// candidate does not resolve inside this window we move on to variants.
const ResolveTimeout = 5 * time.Second

// ResolveRecipient turns user input into a canonical WhatsApp JID.
//
//   - If input already contains `@`, it is parsed as a JID directly (no
//     network call). This lets callers who know the exact JID skip the
//     lookup entirely, which is faster and works even when disconnected.
//   - Otherwise the input is treated as a phone number. Digits are
//     extracted and one or more candidate forms are built (see
//     phoneVariants). Each candidate is probed with `IsOnWhatsApp` using
//     a bounded deadline; the first registered number wins. This avoids
//     whatsmeow's internal (unbounded) usync lookup inside SendMessage,
//     which hangs forever for numbers whose JID differs from their
//     E.164 form (MX/BR/AR mobile prefix quirks).
func (a *App) ResolveRecipient(ctx context.Context, input string) (types.JID, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return types.JID{}, fmt.Errorf("recipient is required")
	}
	if strings.Contains(input, "@") {
		return types.ParseJID(input)
	}

	digits := extractDigits(input)
	if digits == "" {
		return types.JID{}, fmt.Errorf("invalid phone number %q (expected E.164, e.g. +525512345678)", input)
	}

	if a.wa == nil {
		return types.JID{}, fmt.Errorf("whatsapp client not initialized")
	}

	candidates := phoneVariants(digits)
	phones := make([]string, 0, len(candidates))
	for _, c := range candidates {
		phones = append(phones, "+"+c)
	}

	rctx, cancel := context.WithTimeout(ctx, ResolveTimeout)
	defer cancel()

	results, err := a.wa.IsOnWhatsApp(rctx, phones)
	if err != nil {
		return types.JID{}, fmt.Errorf("resolve %q: %w (pass the JID directly with --to user@s.whatsapp.net to skip lookup)", input, err)
	}
	for _, r := range results {
		if r.IsIn && !r.JID.IsEmpty() {
			return r.JID, nil
		}
	}
	return types.JID{}, fmt.Errorf("%q is not registered on WhatsApp (tried %v); pass the JID directly if you know it", input, candidates)
}

// extractDigits strips every non-digit character from s. A leading `+`
// is already covered by this, as are spaces, dashes, and parentheses.
func extractDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// phoneVariants returns plausible digit-only forms of the given number
// to try against IsOnWhatsApp. The input must already be digits-only
// (no `+`, spaces, dashes). The original input is always the first
// candidate.
//
// Rules covered:
//   - Mexico (52): mobiles have a "1" inserted after the country code in
//     WhatsApp JID form (E.164: 52 + 10; JID: 521 + 10).
//   - Brazil (55): mobiles have a "9" prefix on the local number that
//     is sometimes omitted from E.164 (area code + 8 vs area code + 9).
//   - Argentina (54): mobiles have a "9" after the country code in JID
//     form that is not part of the E.164 representation.
//
// For other countries the input is returned as-is. The caller is
// responsible for the network lookup; we just enumerate shapes.
func phoneVariants(digits string) []string {
	out := []string{digits}
	add := func(d string) {
		if d == "" {
			return
		}
		for _, x := range out {
			if x == d {
				return
			}
		}
		out = append(out, d)
	}

	switch {
	// Mexico mobile: 52 + 10 digits (E.164) ↔ 521 + 10 digits (JID).
	case strings.HasPrefix(digits, "521") && len(digits) == 13:
		add("52" + digits[3:])
	case strings.HasPrefix(digits, "52") && len(digits) == 12:
		add("521" + digits[2:])

	// Brazil mobile: 55 + 2-digit area + 8 local (old) ↔ 55 + 2-digit area + 9 + 8 local (new).
	case strings.HasPrefix(digits, "55") && len(digits) == 12:
		add(digits[:4] + "9" + digits[4:])
	case strings.HasPrefix(digits, "55") && len(digits) == 13 && digits[4] == '9':
		add(digits[:4] + digits[5:])

	// Argentina mobile: 54 + 10/11 digits (E.164) ↔ 549 + same (JID).
	case strings.HasPrefix(digits, "549") && len(digits) >= 12:
		add("54" + digits[3:])
	case strings.HasPrefix(digits, "54") && len(digits) >= 12 && digits[2] != '9':
		add("549" + digits[2:])
	}

	return out
}
