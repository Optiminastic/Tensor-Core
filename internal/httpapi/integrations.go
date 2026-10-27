package httpapi

// Integrations an admin configures from the Settings page, rather than from a
// deployment.
//
// A provider's credentials used to live in env/local.env and in Coolify, which
// meant every change - a renamed variable, a different key, a new host - was a
// deploy, and the shop could not fix its own integration without one. They are
// rows now, sealed where they are secret, and the running process reads them
// per brand.
//
// ENV REMAINS THE FALLBACK. A brand that has configured nothing keeps whatever
// the process was started with, so moving a value into the UI is a choice
// rather than a migration everybody has to do at once.

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/delhivery"
	"github.com/Optiminastic/tensor-core/internal/integrations/sarvam"
	"github.com/Optiminastic/tensor-core/internal/integrations/whatsapp"
)

// Provider names, as the URL and the database carry them.
const (
	providerSarvam    = "sarvam"
	providerWhatsApp  = "whatsapp"
	providerDelhivery = "delhivery"
)

// settingField describes one input an admin fills in.
//
// The form is built from this rather than written twice: a field added here
// appears in the UI, is validated on save, and is read by the client, with no
// third place to forget.
type settingField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Help is the one sentence that says where to find this value. Most of
	// these are opaque ids from somebody else's console; a label alone leaves
	// an admin guessing.
	Help string `json:"help"`
	// Secret marks a value that is SEALED AT REST and masked in the form.
	//
	// It is still returned to this endpoint's callers. That is a deliberate
	// choice and a narrow one: integration:manage is held by ADMIN alone (see
	// internal/auth/catalog.go), which is the same role that can disconnect
	// the integration outright, so there is nothing here an admin could not
	// already take away. The trade is that an admin changing the phone number
	// can see the key they are keeping rather than trusting a "Stored" label.
	//
	// The consequence, stated plainly: the key travels to the browser on this
	// page. Everywhere else - logs, errors, any other endpoint - it does not.
	Secret   bool `json:"secret"`
	Required bool `json:"required"`
	// Default is what the running code uses when this setting is blank, and
	// the form pre-fills it.
	//
	// It is NOT a placeholder. An optional field whose absence means "use
	// track.delhivery.com" renders as an empty box beside a Connected badge,
	// which reads as something nobody filled in - so the form shows the value
	// that is actually in force and lets it be changed. Never set on a secret:
	// a default password would be worse than a blank one.
	Default string `json:"default"`
}

// integrationSpec is one provider's form.
type integrationSpec struct {
	Provider string         `json:"provider"`
	Label    string         `json:"label"`
	Summary  string         `json:"summary"`
	Fields   []settingField `json:"fields"`
	// Available is false for a provider whose backend does not exist yet. It
	// is listed so the page is honest about what is planned, and refused on
	// save so nobody stores credentials nothing will read.
	Available bool `json:"available"`
}

// integrationSpecs is the catalogue. One entry per provider.
var integrationSpecs = []integrationSpec{
	{
		Provider: providerSarvam,
		Label:    "Sarvam Voice Agent",
		Summary:  "Places the win-back calls to customers who abandoned a checkout.",
		Fields: []settingField{
			{Key: "api_key", Label: "API key", Secret: true, Required: true,
				Help: "A Voice Agents key (sk_samvaad_…), not a Sarvam model-API key — the wrong one answers 401."},
			{Key: "org_id", Label: "Organisation ID", Required: true,
				Help: "From the agent's own API snippet in Sarvam's console."},
			{Key: "workspace_id", Label: "Workspace ID", Required: true,
				Help: "From the same snippet. An agent in another workspace answers “App not found”."},
			{Key: "agent_id", Label: "Agent ID", Required: true,
				Help: "The app_id from the snippet — not the id in the console URL, which is a different value."},
			{Key: "agent_version", Label: "Agent version", Required: true,
				Help: "An integer, and it must be a COMMITTED version. A draft does not run."},
			{Key: "connection_id", Label: "Telephony connection", Required: true,
				Help: "The Plivo or Vobiz connection onboarded in Sarvam."},
			{Key: "agent_phone_number", Label: "Calls from", Required: true,
				Help: "In full international form, and imported onto that connection. " +
					"This is the number the CALL comes from — not the WhatsApp sender, " +
					"which is a different line configured under WhatsApp Business."},
			{Key: "product_variable", Label: "Cart-item variable", Required: false,
				Default: "cart_item_name",
				Help: "The agent's variable for what was in the basket. Sarvam matches the name " +
					"exactly and silently ignores one it does not know; this has been renamed twice."},
		},
		Available: true,
	},
	{
		Provider: providerDelhivery,
		Label:    "Delhivery",
		Summary:  "Reads carrier tracking, so a parcel that could not be delivered shows up in Tensor.",
		Fields: []settingField{
			{Key: "api_key", Label: "API key", Secret: true, Required: true,
				Help: "From Delhivery's panel under API Setup. A production key; the staging host has its own, and a mismatch returns no shipments rather than an error."},
			{Key: "base_url", Label: "Carrier host", Required: false,
				Default: delhivery.DefaultBaseURL,
				Help: "The production tracking host. Change it only to point at Delhivery's " +
					"staging host, which has its own keys and returns nothing to a production one."},

			// DELHIVERY ONE, a SECOND credential for the same account.
			//
			// The API key above reads tracking, which says where a parcel got
			// to and nothing about whose decision it is waiting on. The words
			// "Need Action", "Auto Re-attempt" and "Return Recommendation"
			// exist only in Delhivery One, behind this machine credential.
			// All four are needed together or the NDR page stays on the
			// tracking view; none of them replaces the API key.
			{Key: "ucp_client_id", Label: "Delhivery One client ID", Required: false,
				Help: "From Delhivery One under API credentials. This is a machine credential, " +
					"not your dashboard login. Without all four One fields the NDR page falls " +
					"back to raw tracking, which cannot show Delhivery's tabs."},
			{Key: "ucp_client_secret", Label: "Delhivery One client secret", Secret: true, Required: false,
				Help: "Issued with the client ID and shown once. A wrong one fails at sign-in " +
					"with Delhivery's own words rather than returning an empty queue."},
			{Key: "ucp_realm", Label: "Delhivery One realm", Required: false,
				Help: "Account-specific, in the form ucp-XXXXXXXXXXX. It is part of the sign-in " +
					"URL, so a wrong realm is refused rather than silently reading another account."},
			{Key: "ucp_client_uuid", Label: "Delhivery One client UUID", Required: false,
				Help: "In the form cms::client::<uuid>. It selects the account on every request " +
					"and must belong to the same account as the realm."},
			{Key: "ucp_gateway_url", Label: "Delhivery One gateway URL", Required: false,
				Help: "The whole URL from the generated MCP config's D1_MCP_URL, ending in /mcp. " +
					"It is per account and there is no default. NOT one.delhivery.com: that host " +
					"serves the dashboard and refuses every write before the request reaches " +
					"Delhivery at all."},
			{Key: "ucp_user_email", Label: "Delhivery One user email", Required: false,
				Help: "Stamped on every action so Delhivery's audit trail names somebody. " +
					"Left blank it sends \"--\", which Delhivery accepts and which reads as " +
					"\"a machine did this\"."},
		},
		Available: true,
	},
	{
		Provider: providerWhatsApp,
		Label:    "WhatsApp Business",
		Summary:  "Sends the win-back message and order updates through Meta's Cloud API.",
		Fields: []settingField{
			{Key: "access_token", Label: "Access token", Secret: true, Required: true,
				Help: "A SYSTEM USER token, not a user token. A user token works identically " +
					"for an hour and then stops - the sends start failing and nothing says so."},
			{Key: "phone_number_id", Label: "Phone number ID", Required: true,
				Help: "The id of the number MESSAGES are sent from — a different line from " +
					"the one the voice agent calls on. The number's id from WhatsApp " +
					"Manager, not the business account's. " +
					"Sending posts to this id; the WABA id there answers 404."},
			{Key: "waba_id", Label: "Business account ID", Required: true,
				Help: "Owns the templates and the delivery analytics."},
			{Key: "app_id", Label: "App ID", Required: true,
				Help: "The Meta app the token belongs to."},
			{Key: "app_secret", Label: "App secret", Secret: true, Required: false,
				Help: "Verifies inbound webhooks, so the WhatsApp bot and inbox need it. " +
					"Sending works without it; nothing Meta posts back is trusted until it is set."},
			{Key: "webhook_verify_token", Label: "Webhook verify token", Secret: true, Required: false,
				Help: "Any long random value. Paste the same value into the webhook setup in " +
					"Meta's app dashboard; Meta sends it back once to prove the callback is yours."},
			{Key: "bot_enabled", Label: "Menu bot", Required: false, Default: "false",
				Help: "true answers every customer message with the menu (Damage concern, " +
					"Explore products, Other). false records messages for the inbox only."},
			{Key: "confirmations_enabled", Label: "Personalisation confirmations", Required: false,
				Default: "false",
				Help: "true messages each newly paid order's customer to confirm the names to be " +
					"printed. Needs the approved order_personalisation_confirm and _reminder " +
					"templates. Orders placed before it was switched on are never messaged."},
			{Key: "test_numbers", Label: "Test numbers only", Required: false,
				Help: "Leave empty to message every customer. Otherwise a comma-separated list " +
					"(e.g. 9876543210, 9123456789): the bot answers only these numbers and " +
					"confirmations go only to them. Everyone else's messages still reach the inbox."},
			{Key: "order_prefix", Label: "Order number prefix", Required: false, Default: "T3DPS-",
				Help: "What a customer's order number is prefixed with in Shopify. A customer " +
					"types 115766; Tensor looks up T3DPS-115766."},
			{Key: "template_name", Label: "Win-back template", Required: true,
				Default: "cart_recovery_personalised",
				Help: "An APPROVED template on that business account. It must take five " +
					"body values — name, products, item count, cart value, discount " +
					"code — and carry a URL button, which is what returns the customer " +
					"to their own basket."},
			{Key: "template_language", Label: "Template language", Required: true,
				Default: "en",
				Help: "Part of the template's identity, not a formatting hint. “en” and " +
					"“en_US” are different templates, and the wrong one answers " +
					"“Template name does not exist in the translation”."},
			// No discount_code field, deliberately. Tensor mints a code PER
			// CUSTOMER at send time - SEETHA10 for Seetha Chinna - valid for
			// one hour and usable once. A shared code configured here would
			// be a public coupon the moment one customer forwarded the
			// message, and it could not carry the customer's own name.
			{Key: "link_host", Label: "Link host", Required: false,
				Default: "the3dprintingstore.in",
				Help: "The domain baked into the approved template’s button. It must be " +
					"the shop’s primary domain, which is where Shopify’s recovery " +
					"links live; on a mismatch Tensor refuses to send rather than point " +
					"the button at the wrong shop."},
			{Key: "api_version", Label: "Graph API version", Required: false,
				Default: "v25.0",
				Help: "Pinned on purpose. Meta retires versions on a schedule, and a floating " +
					"one breaks on a date nobody wrote down."},
		},
		Available: true,
	},
}

func specFor(provider string) (integrationSpec, bool) {
	for _, spec := range integrationSpecs {
		if spec.Provider == provider {
			return spec, true
		}
	}
	return integrationSpec{}, false
}

func (s *Server) registerIntegrations(r *gin.Engine) {
	g := r.Group("/brands/:slug/integrations")
	g.Use(s.guards.RequireUser())
	read := s.guards.RequirePermission(auth.IntegrationManage.Key())

	g.GET("", read, s.listIntegrations)
	g.PUT("/:provider", read, s.saveIntegration)
	g.DELETE("/:provider", read, s.disconnectIntegration)
}

type integrationStatus struct {
	integrationSpec
	Connected bool `json:"connected"`
	// Values holds every setting, secrets included - unsealed on the way out.
	// SecretsSet names which of them are secret, so the form knows to mask
	// them behind a reveal toggle rather than printing a key in plain sight.
	Values     map[string]string `json:"values"`
	SecretsSet []string          `json:"secrets_set"`
	// FromEnvironment says these values came from the process's environment
	// rather than from this page.
	//
	// Worth saying out loud. The form is pre-filled either way, so without it
	// an admin editing a field cannot tell whether they are changing a stored
	// setting or overriding a deployment - and pressing Save does different
	// things in those two cases. It also means Disconnect will NOT make the
	// integration stop working, which would otherwise be baffling.
	FromEnvironment bool `json:"from_environment"`
}

func (s *Server) listIntegrations(c *gin.Context) {
	ctx := c.Request.Context()
	slug := c.Param("slug")

	out := make([]integrationStatus, 0, len(integrationSpecs))
	for _, spec := range integrationSpecs {
		status := integrationStatus{
			integrationSpec: spec,
			Values:          map[string]string{},
			SecretsSet:      []string{},
		}
		rows, err := s.store.Q.ListIntegrationSettings(ctx, gen.ListIntegrationSettingsParams{
			BrandSlug: slug, Provider: spec.Provider,
		})
		if err == nil {
			for _, row := range rows {
				if row.IsSecret {
					status.SecretsSet = append(status.SecretsSet, row.SettingKey)
					// A sealed value that will not open is reported as SET but
					// not shown. Returning the ciphertext into a form field
					// would let somebody save it back as the new plaintext
					// key, which breaks the integration in a way that looks
					// like it worked.
					if opened, openErr := s.secrets.Open(row.SettingValue); openErr == nil {
						status.Values[row.SettingKey] = opened
					}
					continue
				}
				status.Values[row.SettingKey] = row.SettingValue
			}
			status.Connected = len(rows) > 0
		}

		// Nothing stored, but the process was started with values: show those,
		// so the page reflects what is ACTUALLY in use rather than an empty
		// form beside a working integration. Pressing Save then copies them
		// into the database, which is how a deployment migrates off its
		// environment one brand at a time.
		if !status.Connected {
			if env, secrets := s.environmentSettings(spec.Provider); len(env)+len(secrets) > 0 {
				status.Values = env
				status.SecretsSet = secrets
				status.FromEnvironment = true
				status.Connected = true
			}
		}
		out = append(out, status)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

type saveIntegrationRequest struct {
	// Values keyed by field. A secret left blank keeps whatever is stored, so
	// an admin editing the phone number does not have to re-enter the API key
	// they cannot read.
	Values map[string]string `json:"values"`
}

func (s *Server) saveIntegration(c *gin.Context) {
	ctx := c.Request.Context()
	slug := c.Param("slug")

	spec, ok := specFor(c.Param("provider"))
	if !ok {
		detail(c, http.StatusNotFound, "Tensor has no such integration.")
		return
	}
	if !spec.Available {
		detail(c, http.StatusConflict,
			spec.Label+" cannot be connected yet. Storing credentials nothing reads "+
				"would put them at risk for no benefit.")
		return
	}

	var req saveIntegrationRequest
	if !bindJSON(c, &req) {
		return
	}

	known := make(map[string]settingField, len(spec.Fields))
	for _, field := range spec.Fields {
		known[field.Key] = field
	}

	// Which secrets already exist, so a blank one means "keep" rather than
	// "clear" - an admin cannot re-type a key they are not allowed to read.
	existing, _ := s.store.Q.ListIntegrationSettings(ctx, gen.ListIntegrationSettingsParams{
		BrandSlug: slug, Provider: spec.Provider,
	})
	held := make(map[string]bool, len(existing))
	for _, row := range existing {
		held[row.SettingKey] = true
	}

	for _, field := range spec.Fields {
		raw, sent := req.Values[field.Key]
		value := strings.TrimSpace(raw)
		if value == "" {
			if field.Required && !held[field.Key] {
				detail(c, http.StatusUnprocessableEntity, spec.Label+" needs "+field.Label+".")
				return
			}
			// BLANK CLEARS A NON-SECRET FIELD, and leaves a secret alone.
			//
			// "Blank means no change" protects a secret: it comes back
			// masked, so an admin who may not read a token must not be able
			// to wipe it by saving the form. Applied to every field, it also
			// made a plain one impossible to empty - the save returned 200
			// and changed nothing.
			//
			// That silence had teeth. test_numbers restricts the WhatsApp bot
			// to a list of numbers while it is being tried out, and clearing
			// it is how the shop opens the bot to real customers. It could not
			// be cleared: the field went blank on screen, the save reported
			// success, and every customer except the test number went on
			// getting no reply at all.
			// ONLY A FIELD THE FORM ACTUALLY SENT, and never a required or
			// secret one. A caller that posts a single field leaves every
			// other key absent from the map, which is indistinguishable from
			// blank unless the presence of the key is checked - clearing on
			// absence would have wiped phone_number_id and bot_enabled the
			// first time somebody saved one box. Caught by the test below
			// rather than in production.
			if sent && !field.Secret && !field.Required && held[field.Key] {
				if err := s.store.Q.ClearIntegrationSetting(ctx, gen.ClearIntegrationSettingParams{
					BrandSlug: slug, Provider: spec.Provider, SettingKey: field.Key,
				}); err != nil {
					detail(c, http.StatusInternalServerError, "Could not clear "+field.Label+".")
					return
				}
			}
			continue
		}

		if field.Key == "agent_version" {
			if _, err := strconv.Atoi(value); err != nil {
				detail(c, http.StatusUnprocessableEntity,
					"Agent version is a whole number. Sarvam’s console shows it as “v6”; "+
						"the API wants 6.")
				return
			}
		}
		stored := value
		if field.Secret {
			sealed, err := s.secrets.Seal(value)
			if err != nil {
				detail(c, http.StatusInternalServerError, "Could not store that secret.")
				return
			}
			stored = sealed
		}
		by := currentUserID(c)
		if err := s.store.Q.UpsertIntegrationSetting(ctx, gen.UpsertIntegrationSettingParams{
			BrandSlug: slug, Provider: spec.Provider, SettingKey: field.Key,
			SettingValue: stored, IsSecret: field.Secret, UpdatedBy: &by,
		}); err != nil {
			detail(c, http.StatusInternalServerError, "Could not save "+field.Label+".")
			return
		}
	}

	s.listIntegrations(c)
}

func (s *Server) disconnectIntegration(c *gin.Context) {
	spec, ok := specFor(c.Param("provider"))
	if !ok {
		detail(c, http.StatusNotFound, "Tensor has no such integration.")
		return
	}
	if err := s.store.Q.DeleteIntegrationSettings(c.Request.Context(),
		gen.DeleteIntegrationSettingsParams{
			BrandSlug: c.Param("slug"), Provider: spec.Provider,
		}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not disconnect "+spec.Label+".")
		return
	}
	c.Status(http.StatusNoContent)
}

// environmentSettings is what this process was started with, if anything.
//
// Returns the values AND the names of the ones that are secret. An API key in
// the environment is treated exactly like one in the database - see the note
// on settingField.Secret for why this endpoint returns them at all.
func (s *Server) environmentSettings(provider string) (map[string]string, []string) {
	if provider == providerWhatsApp {
		values := map[string]string{}
		put := func(key, value string) {
			if strings.TrimSpace(value) != "" {
				values[key] = value
			}
		}
		put("phone_number_id", s.cfg.WhatsAppPhoneNumberID)
		put("waba_id", s.cfg.WhatsAppWABAID)
		put("app_id", s.cfg.WhatsAppAppID)
		put("api_version", s.cfg.WhatsAppAPIVersion)
		put("template_name", s.cfg.WhatsAppTemplateName)
		put("template_language", s.cfg.WhatsAppTemplateLanguage)
		put("link_host", s.cfg.WinbackLinkHost)

		var secrets []string
		if token := strings.TrimSpace(s.cfg.WhatsAppAccessToken); token != "" {
			secrets = append(secrets, "access_token")
			values["access_token"] = token
		}
		if secret := strings.TrimSpace(s.cfg.WhatsAppAppSecret); secret != "" {
			secrets = append(secrets, "app_secret")
			values["app_secret"] = secret
		}
		// The version and the template have defaults, so they are always
		// present. Connected is judged on the values that identify an ACCOUNT -
		// without those, a row showing "v25.0" and nothing else would read as
		// configured when it can send nothing.
		if s.cfg.WhatsAppPhoneNumberID == "" && len(secrets) == 0 {
			return nil, nil
		}
		return values, secrets
	}
	if provider == providerDelhivery {
		values := map[string]string{}
		if strings.TrimSpace(s.cfg.DelhiveryBaseURL) != "" {
			values["base_url"] = s.cfg.DelhiveryBaseURL
		}
		var secrets []string
		if key := strings.TrimSpace(s.cfg.DelhiveryAPIKey); key != "" {
			secrets = append(secrets, "api_key")
			values["api_key"] = key
		}
		if len(values) == 0 && len(secrets) == 0 {
			return nil, nil
		}
		return values, secrets
	}
	values := map[string]string{}
	put := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			values[key] = value
		}
	}
	put("org_id", s.cfg.SarvamOrgID)
	put("workspace_id", s.cfg.SarvamWorkspaceID)
	put("agent_id", s.cfg.SarvamAgentID)
	put("connection_id", s.cfg.SarvamConnectionID)
	put("agent_phone_number", s.cfg.SarvamAgentPhoneNumber)
	put("product_variable", s.cfg.SarvamProductVariable)
	if s.cfg.SarvamAgentVersion > 0 {
		values["agent_version"] = strconv.Itoa(s.cfg.SarvamAgentVersion)
	}

	var secrets []string
	if key := strings.TrimSpace(s.cfg.SarvamAPIKey); key != "" {
		secrets = append(secrets, "api_key")
		values["api_key"] = key
	}
	if len(values) == 0 && len(secrets) == 0 {
		return nil, nil
	}
	return values, secrets
}

// sarvamFor builds the client for one brand.
//
// The brand's own settings first, the process's environment as the fallback.
// That ordering is what lets the shop move a value into the UI without a
// deploy, and lets a deployment that has configured nothing keep working
// exactly as it did.
//
// Returns nil when neither is complete, which Configured() reports and every
// caller already handles.
func (s *Server) sarvamFor(ctx context.Context, brandSlug string) *sarvam.Client {
	cfg := sarvam.Config{
		BaseURL:          s.cfg.SarvamBaseURL,
		APIKey:           s.cfg.SarvamAPIKey,
		OrgID:            s.cfg.SarvamOrgID,
		WorkspaceID:      s.cfg.SarvamWorkspaceID,
		AgentID:          s.cfg.SarvamAgentID,
		AgentVersion:     s.cfg.SarvamAgentVersion,
		ConnectionID:     s.cfg.SarvamConnectionID,
		AgentPhoneNumber: s.cfg.SarvamAgentPhoneNumber,
	}

	rows, err := s.store.Q.ListIntegrationSettings(ctx, gen.ListIntegrationSettingsParams{
		BrandSlug: brandSlug, Provider: providerSarvam,
	})
	if err == nil {
		for _, row := range rows {
			value := row.SettingValue
			if row.IsSecret {
				opened, err := s.secrets.Open(value)
				if err != nil {
					// A sealed value that will not open is not a reason to
					// fall back to the environment and call somebody with the
					// wrong agent. Leave it unset so Configured() says no.
					continue
				}
				value = opened
			}
			switch row.SettingKey {
			case "api_key":
				cfg.APIKey = value
			case "org_id":
				cfg.OrgID = value
			case "workspace_id":
				cfg.WorkspaceID = value
			case "agent_id":
				cfg.AgentID = value
			case "agent_version":
				if v, err := strconv.Atoi(value); err == nil {
					cfg.AgentVersion = v
				}
			case "connection_id":
				cfg.ConnectionID = value
			case "agent_phone_number":
				cfg.AgentPhoneNumber = value
			}
		}
	}
	return sarvam.New(cfg)
}

// productVariableFor is the agent's name for the cart's contents, per brand.
func (s *Server) productVariableFor(ctx context.Context, brandSlug string) string {
	rows, err := s.store.Q.ListIntegrationSettings(ctx, gen.ListIntegrationSettingsParams{
		BrandSlug: brandSlug, Provider: providerSarvam,
	})
	if err == nil {
		for _, row := range rows {
			if row.SettingKey == "product_variable" && strings.TrimSpace(row.SettingValue) != "" {
				return row.SettingValue
			}
		}
	}
	return s.productVariable()
}

// whatsappFor builds the messaging client for one brand.
//
// Same precedence as sarvamFor and delhiveryFor: the brand's own stored
// settings first, the process's environment as the fallback. Returns nil when
// neither is complete, which Configured() reports.
func (s *Server) whatsappFor(ctx context.Context, brandSlug string) *whatsapp.Client {
	cfg := whatsapp.Config{
		AccessToken:      s.cfg.WhatsAppAccessToken,
		PhoneNumberID:    s.cfg.WhatsAppPhoneNumberID,
		WABAID:           s.cfg.WhatsAppWABAID,
		APIVersion:       s.cfg.WhatsAppAPIVersion,
		TemplateName:     s.cfg.WhatsAppTemplateName,
		TemplateLanguage: s.cfg.WhatsAppTemplateLanguage,
		BaseURL:          s.cfg.WhatsAppBaseURL,
	}
	for key, value := range s.integrationSettings(ctx, brandSlug, providerWhatsApp) {
		switch key {
		case "access_token":
			cfg.AccessToken = value
		case "phone_number_id":
			cfg.PhoneNumberID = value
		case "waba_id":
			cfg.WABAID = value
		case "api_version":
			cfg.APIVersion = value
		case "template_name":
			cfg.TemplateName = value
		case "template_language":
			cfg.TemplateLanguage = value
		}
	}
	// app_secret is deliberately not read: it verifies inbound webhooks and
	// has no part in sending.
	return whatsapp.New(cfg)
}

// winbackLinkFor is the host baked into the approved template's URL button.
//
// Both live beside the credentials rather than in the environment, for the
// same reason productVariableFor does: they are values somebody changes
// without a deploy. The discount code especially - it is a marketing lever,
// it changes with the season, and Shopify does not error on an unknown one,
// so the only fast way to catch a typo is an admin editing it where they see
// the mistake.
func (s *Server) winbackLinkFor(ctx context.Context, brandSlug string) string {
	host := s.cfg.WinbackLinkHost
	for key, value := range s.integrationSettings(ctx, brandSlug, providerWhatsApp) {
		if key == "link_host" {
			host = value
		}
	}
	return strings.TrimSpace(host)
}

// integrationSettings is one brand's stored settings for a provider, unsealed.
//
// Extracted because three resolvers now walk the same rows in the same way,
// and the secret handling is the part that must not drift between them: a
// sealed value that will not open leaves the field UNSET rather than falling
// back to the environment, so a brand never acts with another brand's
// credentials.
func (s *Server) integrationSettings(
	ctx context.Context, brandSlug, provider string,
) map[string]string {
	rows, err := s.store.Q.ListIntegrationSettings(ctx, gen.ListIntegrationSettingsParams{
		BrandSlug: brandSlug, Provider: provider,
	})
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		value := row.SettingValue
		if row.IsSecret {
			opened, openErr := s.secrets.Open(value)
			if openErr != nil {
				continue
			}
			value = opened
		}
		out[row.SettingKey] = value
	}
	return out
}

// delhiveryFor builds the courier client for one brand.
//
// The brand's own stored settings first, the
// process's environment as the fallback. Returns nil when neither has a key,
// which Configured() reports and the handler turns into a page that says the
// courier is not connected rather than an error.
func (s *Server) delhiveryFor(ctx context.Context, brandSlug string) *delhivery.Client {
	cfg := delhivery.Config{
		APIKey:  s.cfg.DelhiveryAPIKey,
		BaseURL: s.cfg.DelhiveryBaseURL,
	}

	rows, err := s.store.Q.ListIntegrationSettings(ctx, gen.ListIntegrationSettingsParams{
		BrandSlug: brandSlug, Provider: providerDelhivery,
	})
	if err == nil {
		for _, row := range rows {
			value := row.SettingValue
			if row.IsSecret {
				opened, openErr := s.secrets.Open(value)
				if openErr != nil {
					// A sealed key that will not open is not a reason to fall
					// back to the environment and read a different account's
					// shipments. Leave it unset so Configured() says no.
					continue
				}
				value = opened
			}
			switch row.SettingKey {
			case "api_key":
				cfg.APIKey = value
			case "base_url":
				cfg.BaseURL = value
			}
		}
	}
	return delhivery.New(cfg)
}

// delhiveryUCPFor builds the Delhivery One client for one brand.
//
// BRAND SETTINGS ONLY, with no environment fallback - unlike delhiveryFor.
// The tracking key has a legacy env var because it predates the settings
// screen; this credential never did, and an environment fallback here would
// let one brand's page quietly read another brand's exception queue.
//
// Nil when any required part is missing, which is the normal case for a brand
// that has only ever had the tracking key. The gateway URL counts: without it
// there is nowhere to send a request, and there is no default to fall back on.
func (s *Server) delhiveryUCPFor(ctx context.Context, brandSlug string) *delhivery.UCPClient {
	var cfg delhivery.UCPConfig

	rows, err := s.store.Q.ListIntegrationSettings(ctx, gen.ListIntegrationSettingsParams{
		BrandSlug: brandSlug, Provider: providerDelhivery,
	})
	if err != nil {
		return nil
	}
	for _, row := range rows {
		value := row.SettingValue
		if row.IsSecret {
			opened, openErr := s.secrets.Open(value)
			if openErr != nil {
				continue
			}
			value = opened
		}
		switch row.SettingKey {
		case "ucp_client_id":
			cfg.ClientID = value
		case "ucp_client_secret":
			cfg.ClientSecret = value
		case "ucp_realm":
			cfg.Realm = value
		case "ucp_client_uuid":
			cfg.HQClientID = value
		case "ucp_gateway_url":
			cfg.GatewayURL = value
		case "ucp_user_email":
			cfg.UserEmail = value
		}
	}
	return delhivery.NewUCP(cfg)
}
