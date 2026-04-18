package arboxapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Sample response shape based on oribenez/auto-enroll-arbox usage:
//
//	responseData.data.token / .data.refreshToken
const sampleLoginBody = `{
  "data": {
    "token": "eyJhbGciOiJIUzI1NiJ9.payload.sig",
    "refreshToken": "refresh-xyz",
    "id": 4242,
    "first_name": "Assaf",
    "last_name":  "Manzur",
    "email":      "assaf@example.com"
  }
}`

func TestParseLoginResponse_NestedData(t *testing.T) {
	r, err := parseLoginResponse([]byte(sampleLoginBody))
	if err != nil {
		t.Fatal(err)
	}
	if r.Token == "" || !strings.HasPrefix(r.Token, "eyJ") {
		t.Errorf("token not parsed: %q", r.Token)
	}
	if r.RefreshToken != "refresh-xyz" {
		t.Errorf("refresh token: %q", r.RefreshToken)
	}
	if r.UserID != 4242 {
		t.Errorf("user id: %d", r.UserID)
	}
	if r.FirstName != "Assaf" {
		t.Errorf("first name: %q", r.FirstName)
	}
}

func TestParseLoginResponse_TopLevelFallback(t *testing.T) {
	body := `{"token":"abc","refreshToken":"def"}`
	r, err := parseLoginResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if r.Token != "abc" || r.RefreshToken != "def" {
		t.Errorf("top-level parse failed: %+v", r)
	}
}

func TestParseLoginResponse_NoToken(t *testing.T) {
	body := `{"data":{"error":"wrong"}}`
	_, err := parseLoginResponse([]byte(body))
	if !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("expected ErrLoginFailed, got %v", err)
	}
}

func TestLogin_Success_SendsCorrectRequest(t *testing.T) {
	var gotMethod, gotPath, gotCT, gotAccept, gotUA string
	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleLoginBody))
	}))
	defer srv.Close()

	c := New(srv.URL)
	r, err := c.LoginAndStore(context.Background(), "assaf@example.com", "sekret")
	if err != nil {
		t.Fatal(err)
	}

	if gotMethod != "POST" {
		t.Errorf("method: %s", gotMethod)
	}
	if gotPath != "/api/v2/user/login" {
		t.Errorf("path: %s", gotPath)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("content-type: %s", gotCT)
	}
	if gotAccept == "" {
		t.Errorf("accept header missing")
	}
	if gotUA == "" {
		t.Errorf("user-agent header missing")
	}
	if gotBody["email"] != "assaf@example.com" || gotBody["password"] != "sekret" {
		t.Errorf("body: %#v", gotBody)
	}

	if c.Token != r.Token || c.RefreshToken != r.RefreshToken {
		t.Errorf("tokens not stored on client")
	}
}

func TestLogin_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad creds"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.Login(context.Background(), "a@b.com", "bad")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}
