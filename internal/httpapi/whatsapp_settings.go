package httpapi

import (
	"context"
	"strconv"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/whatsappbot/phone"
)

// defaultOrderPrefix is the store's Shopify order-name prefix when a brand has
// not set one.
const defaultOrderPrefix = "T3DPS-"

// whatsappBotSettings are the receiving-side settings of one brand's WhatsApp
// integration. Sending credentials stay with whatsappFor.
type whatsappBotSettings struct {
	AppSecret            string
	VerifyToken          string
	OrderPrefix          string
	BotEnabled           bool
	ConfirmationsEnabled bool
	// TestNumbers, when set, limits every automated message to these numbers:
	// the bot answers only them and confirmations go only to them. Everyone
	// else's messages still reach the inbox. It is how the bot is tried on the
	// live number without answering real customers.
	TestNumbers []string
}

// answers reports whether the bot replies to this customer.
func (b whatsappBotSettings) answers(waID string) bool {
	return b.BotEnabled && b.mayMessage(waID)
}

// mayMessage reports whether automated messages may go to this number.
func (b whatsappBotSettings) mayMessage(waID string) bool {
	if len(b.TestNumbers) == 0 {
		return true
	}
	for _, n := range b.TestNumbers {
		if phone.Match(n, waID) {
			return true
		}
	}
	return false
}

// parseTestNumbers reads a comma- or semicolon-separated list, dropping
// anything that is not a phone number.
func parseTestNumbers(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' }) {
		if n, ok := phone.Normalize(part); ok {
			out = append(out, n)
		}
	}
	return out
}

// whatsappBotSettingsFor reads them with the same precedence as whatsappFor:
// the brand's stored settings, then the process environment for the secret.
// Both switches default OFF, so a brand only gets automated messages when an
// admin has said so.
func (s *Server) whatsappBotSettingsFor(ctx context.Context, brandSlug string) whatsappBotSettings {
	out := whatsappBotSettings{
		AppSecret:   strings.TrimSpace(s.cfg.WhatsAppAppSecret),
		OrderPrefix: defaultOrderPrefix,
	}
	for key, value := range s.integrationSettings(ctx, brandSlug, providerWhatsApp) {
		value = strings.TrimSpace(value)
		switch key {
		case "app_secret":
			out.AppSecret = value
		case "webhook_verify_token":
			out.VerifyToken = value
		case "order_prefix":
			if value != "" {
				out.OrderPrefix = value
			}
		case "bot_enabled":
			out.BotEnabled = settingOn(value)
		case "confirmations_enabled":
			out.ConfirmationsEnabled = settingOn(value)
		case "test_numbers":
			out.TestNumbers = parseTestNumbers(value)
		}
	}
	return out
}

func settingOn(value string) bool {
	on, err := strconv.ParseBool(strings.ToLower(value))
	return err == nil && on
}
