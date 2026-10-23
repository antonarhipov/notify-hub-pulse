package pulse

import "testing"

func TestScenarioTraffic(t *testing.T) {
	for _, tc := range []struct {
		name     string
		channels map[string]int
	}{
		{"normal", map[string]int{"email": 60, "sms": 20, "push": 20}},
		{"campaign", map[string]int{"email": 90, "sms": 5, "push": 5}},
		{"duplicates", map[string]int{"email": 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scenario, err := NewScenario(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]int{}
			for i := range 100 {
				n := scenario.Notification(i)
				if n.Recipient == "" || n.Body == "" || n.Subject == "" {
					t.Fatalf("empty content: %+v", n)
				}
				if n != scenario.Notification(i) {
					t.Fatal("scenario is not repeatable")
				}
				got[n.Channel]++
			}
			for channel, want := range tc.channels {
				if got[channel] != want {
					t.Errorf("%s count = %d, want %d", channel, got[channel], want)
				}
			}
		})
	}
	if _, err := NewScenario("typo"); err == nil {
		t.Fatal("unknown scenario accepted")
	}
}

func TestDuplicateScenarioHasSmallVariations(t *testing.T) {
	scenario, _ := NewScenario("duplicates")
	first := scenario.Notification(0)
	for i := 1; i <= 3; i++ {
		n := scenario.Notification(i)
		if n.Channel != first.Channel || n.Subject != first.Subject || n.Recipient != first.Recipient {
			t.Fatalf("duplicate target changed: %+v", n)
		}
		if n.Body == first.Body {
			t.Fatal("missing small variation")
		}
	}
}
