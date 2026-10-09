package httpapi

// Ringing one customer about one cart.
//
// MANUAL ONLY, on purpose. A person opens the Abandoned Checkouts page, reads
// a row, and presses Call. Nothing here polls Shopify, scores a customer or
// decides that somebody deserves a phone call - that is the difference between
// this and an autodialler, and it is the difference worth keeping until the
// script has been heard on real calls and the consent rules are settled.
//
// The automatic poller, the eligibility rules and the discount code all sit on
// top of this later. None of them change its shape.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/integrations/sarvam"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

func (s *Server) registerVoiceCalls(r *gin.Engine) {
	g := r.Group("/voice")
	g.Use(s.guards.RequireUser())
	// integration:manage, the permission that already governs connecting
	// external services. Placing a call on the company's behalf belongs with
	// that rather than with production work: an Operator who may run a printer
	// has no business phoning customers.
	manage := s.guards.RequirePermission(auth.IntegrationManage.Key())

	g.GET("/status", manage, s.voiceStatus)
	g.POST("/calls", manage, s.placeVoiceCall)
}

type voiceStatusResponse struct {
	Configured bool `json:"configured"`
	// FromNumber is the number customers will see. Worth showing before anyone
	// presses Call: a wrong caller ID is the kind of thing nobody notices
	// until a customer rings it back.
	FromNumber string `json:"from_number"`
}

// voiceStatus says whether a call can be placed at all, and from what number.
//
// So the page can explain itself before anybody presses anything: "voice
// calling is not configured" is a setup problem with a known fix, and finding
// it out by pressing Call and reading a 503 is a worse way to learn it.
func (s *Server) voiceStatus(c *gin.Context) {
	c.JSON(http.StatusOK, voiceStatusResponse{
		Configured: s.sarvam.Configured(),
		FromNumber: s.sarvam.AgentPhoneNumber(),
	})
}

// placeVoiceCallRequest is one customer, read off a row the operator is
// looking at.
//
// The fields are what the agent SAYS, which is why they are validated for
// being speakable rather than matched against a catalogue. The customer's
// number is normalised the way people actually write numbers down.
//
// The JSON names are the AGENT'S variable names, spelled exactly as they are
// defined on it in Sarvam's console. Renaming anything on the way through is
// what produced the first bug here - Tensor sent "product" to an agent whose
// script reads {{product_name}}, so every call named a placeholder instead of
// the thing in the basket.
type placeVoiceCallRequest struct {
	CustomerName   string `json:"customer_name" binding:"required,min=1,max=80"`
	CustomerNumber string `json:"customer_number" binding:"required,min=8,max=20"`
	ProductName    string `json:"product_name" binding:"max=160"`
	CartValue      string `json:"cart_value" binding:"max=24"`
	ItemCount      int    `json:"item_count" binding:"omitempty,min=0,max=999"`
}

type placeVoiceCallResponse struct {
	// AttemptID is Sarvam's correlation key: the completion webhook carries
	// the same value, and the call logs are found by it.
	AttemptID string `json:"attempt_id"`
}

// agentVariables maps one request onto the agent's script.
//
// Pure, and separate from the handler, so the one thing that silently breaks a
// call - a variable name that does not exist on the agent - is covered by a
// test that needs no database and no phone.
func (s *Server) agentVariables(req placeVoiceCallRequest) map[string]string {
	// The agent's own variable names, copied from its Build > Variables page.
	// Sarvam matches them EXACTLY and case-sensitively, and refuses the call
	// naming any it does not recognise - so these four are the agent's
	// contract, not a free-form bag.
	//
	// The product's variable name comes from configuration, because this one
	// keeps moving. It shipped as "Product_name" - a capital P among three
	// lowercase siblings - and was later renamed, which broke every call with
	// "Agent variables '{'Product_name'}' not found". Sarvam matches names
	// exactly and refuses the whole call over one it does not know, so a name
	// that lives on somebody else's console must not need a deploy to follow.
	// It has since moved again, to cart_item_name, which is the default.
	// Override with SARVAM_PRODUCT_VARIABLE.
	//
	// There is deliberately no gender: this agent has no such variable, and
	// sending one would have every call refused.
	//
	// An empty value is omitted rather than sent blank, so the agent falls back
	// to its own default for that variable.
	variables := map[string]string{
		"customer_name": strings.TrimSpace(req.CustomerName),
	}
	if p := strings.TrimSpace(req.ProductName); p != "" {
		variables[s.productVariable()] = p
	}
	if v := strings.TrimSpace(req.CartValue); v != "" {
		variables["cart_value"] = v
	}
	if req.ItemCount > 0 {
		variables["item_count"] = itoa(req.ItemCount)
	}
	return variables
}

func (s *Server) placeVoiceCall(c *gin.Context) {
	if !s.sarvam.Configured() {
		detail(c, http.StatusServiceUnavailable,
			"Voice calling is not configured. Set SARVAM_API_KEY, SARVAM_ORG_ID, "+
				"SARVAM_WORKSPACE_ID, SARVAM_AGENT_ID, SARVAM_CONNECTION_ID and "+
				"SARVAM_AGENT_PHONE_NUMBER on this service.")
		return
	}

	var req placeVoiceCallRequest
	if !bindJSON(c, &req) {
		return
	}

	number := normaliseIndianMobile(req.CustomerNumber)
	if number == "" {
		detail(c, http.StatusUnprocessableEntity,
			"That does not look like a phone number we can dial. Use the full "+
				"international form, for example +919876543210.")
		return
	}

	variables := s.agentVariables(req)

	result, err := s.sarvam.Call(c.Request.Context(), sarvam.CallRequest{
		CustomerNumber: number,
		Variables:      variables,
	})
	if err != nil {
		// Sarvam's own sentence. Its refusals name the missing number or the
		// wrong field, and the operator is the person who can act on that.
		detail(c, http.StatusBadGateway, err.Error())
		return
	}

	// The customer's number is NOT logged. It is personal data, the attempt id
	// is enough to find the call in Sarvam, and a log line is the easiest place
	// for a phone number to end up somewhere nobody meant it to.
	obs.FromContext(c.Request.Context()).Info("placed a win-back call",
		"attempt", result.AttemptID, "product", req.ProductName)

	c.JSON(http.StatusAccepted, placeVoiceCallResponse{AttemptID: result.AttemptID})
}

// productVariable is the agent's name for the cart's contents.
func (s *Server) productVariable() string {
	if name := strings.TrimSpace(s.cfg.SarvamProductVariable); name != "" {
		return name
	}
	return "cart_item_name"
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// normaliseIndianMobile puts a number into the E.164 form the telephony
// provider dials.
//
// Operators type phone numbers the way they are written down - "98765 43210",
// "098765 43210", "+91 98765-43210" - and the API wants "+919876543210".
// Rejecting the first three would be technically correct and would make the
// page infuriating, so the common Indian shapes are accepted and normalised.
//
// A number that already carries another country code passes through untouched:
// this is a guess about Indian mobiles, not a rule that Tensor only ever calls
// India.
func normaliseIndianMobile(raw string) string {
	var digits strings.Builder
	plus := strings.HasPrefix(strings.TrimSpace(raw), "+")
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	switch {
	case d == "":
		return ""
	case plus:
		return "+" + d
	case len(d) == 10:
		return "+91" + d
	case len(d) == 11 && strings.HasPrefix(d, "0"):
		return "+91" + d[1:]
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		return "+" + d
	case len(d) >= 11:
		return "+" + d
	default:
		return ""
	}
}
