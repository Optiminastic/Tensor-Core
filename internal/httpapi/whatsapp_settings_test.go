package httpapi

import "testing"

func TestBotAnswers(t *testing.T) {
	off := whatsappBotSettings{}
	everyone := whatsappBotSettings{BotEnabled: true}
	listed := whatsappBotSettings{BotEnabled: true, TestNumbers: parseTestNumbers("+91 90000 00001, 09000000002;bad")}

	if off.answers("919000000001") {
		t.Error("a disabled bot answered")
	}
	if !everyone.answers("919876543210") {
		t.Error("with no test list the bot answers everyone")
	}
	if !listed.answers("919000000001") || !listed.answers("919000000002") {
		t.Errorf("listed numbers not answered: %v", listed.TestNumbers)
	}
	if listed.answers("919876543210") {
		t.Error("an unlisted number was answered")
	}
}
