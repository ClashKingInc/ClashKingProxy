package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestLoadKeysFromEnvParsesTrimmedKeys(t *testing.T) {
	t.Setenv("COC_KEYS", " first , , second,third  ")

	got := loadKeysFromEnv()
	want := []string{"first", "second", "third"}

	if len(got) != len(want) {
		t.Fatalf("loadKeysFromEnv() length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("loadKeysFromEnv()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadKeysFromEnvExitsWithWhitespaceOnlyKeys(t *testing.T) {
	if os.Getenv("TEST_LOAD_KEYS_FROM_ENV_EXIT") == "1" {
		loadKeysFromEnv()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestLoadKeysFromEnvExitsWithWhitespaceOnlyKeys")
	cmd.Env = append(os.Environ(),
		"TEST_LOAD_KEYS_FROM_ENV_EXIT=1",
		"COC_KEYS= , , ",
	)

	err := cmd.Run()
	if err == nil {
		t.Fatal("loadKeysFromEnv() did not exit")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("command error type = %T, want *exec.ExitError", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("exit code = %d, want %d", exitErr.ExitCode(), 1)
	}
}

func TestBuildHTTPClient(t *testing.T) {
	client := buildHTTPClient()

	if client.Timeout != 20*time.Second {
		t.Fatalf("client timeout = %s, want %s", client.Timeout, 20*time.Second)
	}

	err := client.CheckRedirect(httptest.NewRequest(http.MethodGet, "https://example.com", nil), nil)
	if err != http.ErrUseLastResponse {
		t.Fatalf("CheckRedirect() = %v, want %v", err, http.ErrUseLastResponse)
	}
}

func TestNewProxyServerSetsDefaults(t *testing.T) {
	server := newProxyServer(nil, nil, []string{"first"}, "https://dev.example.com")

	if server.client == nil {
		t.Fatal("newProxyServer() client = nil")
	}
	if server.stats == nil {
		t.Fatal("newProxyServer() stats = nil")
	}
	if server.prodBaseURL != prodBaseURL {
		t.Fatalf("prodBaseURL = %q, want %q", server.prodBaseURL, prodBaseURL)
	}
	if server.devBaseURL != "https://dev.example.com/" {
		t.Fatalf("devBaseURL = %q, want %q", server.devBaseURL, "https://dev.example.com/")
	}
}
