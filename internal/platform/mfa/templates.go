package mfa

import (
	"fmt"
	"time"
)

// renderEmailPin builds the HTML body for a one-time email PIN. Kept simple
// and inline (mirrors the password-reset email style); a richer templating
// layer can replace this later without touching the service.
func renderEmailPin(pin string, ttl time.Duration) string {
	mins := int(ttl.Minutes())
	return fmt.Sprintf(
		"<p>Your verification code is:</p>"+
			"<p style=\"font-size:24px;font-weight:bold;letter-spacing:3px\">%s</p>"+
			"<p>This code expires in %d minutes.</p>"+
			"<p>If you did not try to sign in, you can ignore this email.</p>",
		pin, mins)
}
