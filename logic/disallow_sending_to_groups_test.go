package logic

import "testing"

func TestCheckSendRefusesGroups(t *testing.T) {
	if err := CheckSend("120363021234567890@g.us"); err == nil {
		t.Fatal("CheckSend allowed a group JID; the group-send block is broken")
	}
}

func TestCheckSendAllowsIndividuals(t *testing.T) {
	for _, to := range []string{"966512345678@s.whatsapp.net", "966512345678"} {
		if err := CheckSend(to); err != nil {
			t.Fatalf("CheckSend refused individual %q: %v", to, err)
		}
	}
}
