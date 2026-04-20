package arboxapi

import "testing"

func TestYouStatusDetail_Booked(t *testing.T) {
	booked := 42
	c := Class{UserBookedID: &booked}
	if got := c.YouStatusDetail(); got != "BOOKED" {
		t.Fatalf("got %q want BOOKED", got)
	}
}

func TestYouStatusDetail_WaitlistWithPosition(t *testing.T) {
	standby := 999
	c := Class{
		UserStandByID:   &standby,
		StandBy:         7,
		StandByPosition: 3,
	}
	if got := c.YouStatusDetail(); got != "WAITLIST 3/7" {
		t.Fatalf("got %q want WAITLIST 3/7", got)
	}
}

func TestYouStatusDetail_WaitlistPositionOnlyFromRaw(t *testing.T) {
	c := Class{Raw: map[string]any{
		"user_in_standby":   float64(123),
		"stand_by_position": float64(2),
	}}
	c.StandBy = 5
	if got := c.YouStatusDetail(); got != "WAITLIST 2/5" {
		t.Fatalf("got %q want WAITLIST 2/5 (raw fallback)", got)
	}
}

func TestYouStatusDetail_WaitlistUnknownPosition(t *testing.T) {
	standby := 999
	c := Class{UserStandByID: &standby, StandBy: 4}
	if got := c.YouStatusDetail(); got != "WAITLIST" {
		t.Fatalf("got %q want plain WAITLIST", got)
	}
}

func TestYouStatusDetail_NotRelatedToUser(t *testing.T) {
	c := Class{StandBy: 10, StandByPosition: 3}
	if got := c.YouStatusDetail(); got != "" {
		t.Fatalf("got %q want empty (user not on waitlist)", got)
	}
}
