package httpapi

import (
	"context"
	"strconv"
	"strings"
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
		}
	}
	return out
}

func settingOn(value string) bool {
	on, err := strconv.ParseBool(strings.ToLower(value))
	return err == nil && on
}
