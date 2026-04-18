package arboxapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBookClass_DryRunBuildsPayload(t *testing.T) {
	c := New("https://example.invalid")
	c.Token = "t"
	res, err := c.BookClass(context.Background(), 42, 57344579, true)
	if err != nil {
		t.Fatalf("dry-run should not error: %v", err)
	}
	if res.Sent {
		t.Fatal("dry-run should not mark Sent=true")
	}
	if res.Method != "POST" {
		t.Errorf("method: %q", res.Method)
	}
	if !strings.HasSuffix(res.URL, "/api/v2/scheduleUser/insert") {
		t.Errorf("url: %q", res.URL)
	}
	var got BookRequest
	if err := json.Unmarshal([]byte(res.RequestJSON), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if got.MembershipUserID != 42 || got.ScheduleID != 57344579 {
		t.Errorf("body: %+v", got)
	}
	if got.Extras != nil {
		t.Errorf("extras should be null, got %v", got.Extras)
	}
}

func TestCancelBooking_DryRunBuildsPayload(t *testing.T) {
	c := New("https://example.invalid")
	c.Token = "t"
	res, err := c.CancelBooking(context.Background(), 164884630, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent {
		t.Fatal("dry-run should not send")
	}
	if !strings.Contains(res.RequestJSON, `"schedule_user_id"`) ||
		!strings.Contains(res.RequestJSON, `164884630`) {
		t.Errorf("body missing schedule_user_id: %s", res.RequestJSON)
	}
}

func TestExtractErrorMessage(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`{"error":{"messageToUser":"class is full"}}`, "class is full"},
		{`{"error":{"message":"bad request"}}`, "bad request"},
		{`{"message":"try again"}`, "try again"},
		{`{"data":{}}`, ""},
		{``, ""},
	}
	for _, tc := range cases {
		got := extractErrorMessage([]byte(tc.in))
		if got != tc.want {
			t.Errorf("extractErrorMessage(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUserFeed_Parses(t *testing.T) {
	body := []byte(`{"scheduleUserStatus":{"results":{"past":"3","future":"2"}}}`)
	var f UserFeed
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatal(err)
	}
	if f.PastBookings() != 3 || f.FutureBookings() != 2 {
		t.Errorf("feed: past=%d future=%d", f.PastBookings(), f.FutureBookings())
	}
}
