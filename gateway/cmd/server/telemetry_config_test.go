package main

import "testing"

func TestTelemetryConfig(t *testing.T) {
	c := Default()
	if !c.Telemetry.Enabled || c.Telemetry.RetentionDays != 7 || c.Telemetry.MaxEvents != 100000 || c.Telemetry.Dir != "./data/telemetry" {
		t.Fatalf("defaults %+v", c.Telemetry)
	}
	for _, days := range []int{0, -1, 366} {
		c := Default()
		c.Telemetry.RetentionDays = days
		if c.normalize() == nil {
			t.Fatalf("accepted days %d", days)
		}
	}
	c = Default()
	c.Telemetry.MaxEvents = 100001
	if c.normalize() == nil {
		t.Fatal("accepted unbounded events")
	}
}
