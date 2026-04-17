package cli

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestStatusCommand_UsesObserverAPI(t *testing.T) {
	state := &replState{
		loggedIn: true,
		token:    "tok",
		baseURL:  "http://contextos.test",
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/api/v1/observer/system" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       ioutil.NopCloser(strings.NewReader(`{"status":"ok","node_id":"node-1"}`)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       ioutil.NopCloser(strings.NewReader(`{"message":"not found"}`)),
				Header:     make(http.Header),
			}, nil
		})},
	}
	out := captureStdout(t, func() {
		handleStatus(state, []string{"system"})
	})
	if !strings.Contains(out, "node-1") {
		t.Fatalf("expected status output to contain node-1, got %q", out)
	}
}
