package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// rewriteRT rewrites outgoing requests to target the test server while preserving the original path+query.
type rewriteRT struct {
	base *url.URL
	rt   http.RoundTripper
}

func (r rewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = r.base.Scheme
	newReq.URL.Host = r.base.Host
	// keep the original Path/RawQuery
	return r.rt.RoundTrip(newReq)
}

func installTestClient(t *testing.T, srv *httptest.Server) func() {
	t.Helper()
	orig := http.DefaultClient
	u, _ := url.Parse(srv.URL)
	http.DefaultClient = &http.Client{
		Transport: rewriteRT{base: u, rt: http.DefaultTransport},
	}
	return func() { http.DefaultClient = orig }
}

func TestGetGoogleAccessToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GoogleTokenResponse{
			AccessToken: "tok-123",
			ExpiresIn:   3600,
			TokenType:   "Bearer",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	restore := installTestClient(t, srv)
	defer restore()

	token, err := GetGoogleAccessToken("id", "secret", "refresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "tok-123" {
		t.Fatalf("token = %q; want %q", token, "tok-123")
	}
}

func TestGetGoogleAccessToken_NoAccessTokenInResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// valid JSON but no access_token
		w.Write([]byte(`{"expires_in":3600}`))
	}))
	defer srv.Close()
	restore := installTestClient(t, srv)
	defer restore()

	_, err := GetGoogleAccessToken("id", "secret", "refresh")
	if err == nil {
		t.Fatalf("expected error when access_token missing")
	}
}

func TestGetGoogleAccessToken_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not-json`))
	}))
	defer srv.Close()
	restore := installTestClient(t, srv)
	defer restore()

	_, err := GetGoogleAccessToken("id", "secret", "refresh")
	if err == nil {
		t.Fatalf("expected error on bad json")
	}
}
