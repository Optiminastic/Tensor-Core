package httpapi

// The language the abandoned-checkout agent speaks, from the customer's state.
//
// The agent opened every call in Hindi. A customer in Chennai or Pune was
// greeted in a language that is not theirs, and the agent only switched once
// they answered in their own. Shopify's abandoned checkout carries the state
// the customer typed at the address step, so Tensor picks the state's own
// language and the agent opens the call in it.
//
// Only the languages the agent speaks are named: Hindi, English, Bengali,
// Gujarati, Kannada, Malayalam, Tamil, Telugu, Punjabi, Odia and Marathi.
// Every other state - and a checkout abandoned before the address step, which
// has no state - opens in Hindi, the agent's own default.
//
// Keys are normalised (lower case, single spaces, no dots), and Shopify's
// two-letter codes and the older spellings are listed beside the names,
// because the field is whatever the checkout form stored.

import "strings"

// defaultCallLanguage is what a state without a supported language of its own
// - or no state at all - opens in.
const defaultCallLanguage = "Hindi"

var stateCallLanguages = map[string]string{
	"maharashtra": "Marathi", "mh": "Marathi",
	"gujarat": "Gujarati", "gj": "Gujarati",
	"west bengal": "Bengali", "westbengal": "Bengali", "wb": "Bengali",
	"tripura": "Bengali", "tr": "Bengali",
	"karnataka": "Kannada", "ka": "Kannada",
	"kerala": "Malayalam", "kl": "Malayalam",
	"lakshadweep": "Malayalam", "ld": "Malayalam",
	"tamil nadu": "Tamil", "tamilnadu": "Tamil", "tn": "Tamil",
	"puducherry": "Tamil", "pondicherry": "Tamil", "py": "Tamil",
	"andhra pradesh": "Telugu", "andhrapradesh": "Telugu", "ap": "Telugu",
	"telangana": "Telugu", "ts": "Telugu", "tg": "Telugu",
	"punjab": "Punjabi", "pb": "Punjabi",
	"odisha": "Odia", "orissa": "Odia", "or": "Odia", "od": "Odia",
}

// callLanguageForState is the language a call to a customer in state opens in.
func callLanguageForState(state string) string {
	key := strings.Join(strings.Fields(strings.ToLower(strings.ReplaceAll(state, ".", ""))), " ")
	if lang, ok := stateCallLanguages[key]; ok {
		return lang
	}
	return defaultCallLanguage
}
