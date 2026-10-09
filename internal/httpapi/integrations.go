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
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/delhivery"
)

// Provider names, as the URL and the database carry them.
const (
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
		},
		Available: true,
	},
	{
		Provider: providerWhatsApp,
		Label:    "WhatsApp Business",
		Summary:  "Order updates, personalisation confirmations and the win-back discount code.",
		// Deliberately unavailable. The backend does not exist, and a form
		// that stores a permanent access token nothing reads is a credential
		// at risk for no benefit. It is listed so the page says what is
		// planned rather than pretending the plan is a secret.
		Fields:    nil,
		Available: false,
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
		value := strings.TrimSpace(req.Values[field.Key])
		if value == "" {
			if field.Required && !held[field.Key] {
				detail(c, http.StatusUnprocessableEntity, spec.Label+" needs "+field.Label+".")
				return
			}
			continue
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
	return nil, nil
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
