package logic

import "testing"

func TestCheckSendAllowsIndividuals(t *testing.T) {
	if err := CheckSend("966512345678@s.whatsapp.net"); err != nil {
		t.Fatalf("CheckSend refused an individual JID: %v", err)
	}
}

// The allowlist refuses everything that is not an @s.whatsapp.net individual:
// groups, channels, hidden users (@lid), and bare numbers.
func TestCheckSendRefusesNonIndividuals(t *testing.T) {
	for _, to := range []string{
		"120363021234567890@g.us",       // group
		"120363021234567890@newsletter", // channel
		"231142506668036@lid",           // hidden user
		"966512345678",                  // bare number, no server
	} {
		if err := CheckSend(to); err == nil {
			t.Fatalf("CheckSend allowed a non-individual %q; allowlist is broken", to)
		}
	}
}
