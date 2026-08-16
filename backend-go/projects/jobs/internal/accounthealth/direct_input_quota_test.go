package accounthealth

import "testing"

func TestDirectQuotaExceededUsesInclusiveLimit(t *testing.T) {
	limits, err := ParseDirectQuotaLimits(`{"hourly":{"enabled":true,"hours":6,"limit":3},"daily":{"enabled":true,"limit":5}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !DirectQuotaExceeded(limits, DirectQuotaCosts{Hourly: 3, Daily: 0}) {
		t.Fatal("cost equal to the limit must be exceeded")
	}
	if DirectQuotaExceeded(limits, DirectQuotaCosts{Hourly: 2.99, Daily: 4.99}) {
		t.Fatal("cost below all limits must remain eligible")
	}
}

func TestDirectQuotaRejectsInvalidEnabledWindow(t *testing.T) {
	if _, err := ParseDirectQuotaLimits(`{"hourly":{"enabled":true,"hours":0,"limit":1}}`); err == nil {
		t.Fatal("expected invalid hourly window to fail")
	}
}
