package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHelloQuickstart(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(newRouter(logger))
	defer server.Close()

	client := server.Client()
	client.Timeout = 2 * time.Second
	response, err := client.Get(server.URL + "/hello/Ada")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("content type = %q, want application/json", contentType)
	}

	var body helloOutput
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Message != "hello, Ada" {
		t.Fatalf("message = %q, want %q", body.Message, "hello, Ada")
	}
}
