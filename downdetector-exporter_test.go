package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func assertEqual[T comparable](t *testing.T, expected, actual T) {
	t.Helper()
	if expected != actual {
		t.Fatalf("expected %v, got: %v", expected, actual)
	}
}

func TestAddStringFlagUsesEnvironmentAndSupportsAliases(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"--company_ids=from-cli"}},
		{name: "short alias", args: []string{"-i", "from-cli"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("COMPANY_IDS", "from-env")

			var value string
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			addStringFlag(flags, &value, "company_ids", "i", "COMPANY_IDS", "fallback", "company IDs")

			assertEqual(t, "from-env", value)
			assertNoError(t, flags.Parse(tt.args))
			assertEqual(t, "from-cli", value)
		})
	}
}

func TestLoadCredentialsReadsCompanyIDs(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		companyIDs string
	}{
		{name: "unquoted IDs", yaml: "company_ids: 123,456\n", companyIDs: "123,456"},
		{name: "quoted IDs", yaml: "company_ids: \"123,456\"\n", companyIDs: "123,456"},
		{name: "single ID", yaml: "company_ids: 123\n", companyIDs: "123"},
		{name: "credentials only"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credentialsFile := filepath.Join(t.TempDir(), "credentials.yaml")
			content := "username: test-user\npassword: test-password\n" + tt.yaml
			assertNoError(t, os.WriteFile(credentialsFile, []byte(content), 0o600))

			credentials, err := loadCredentials(credentialsFile)

			assertNoError(t, err)
			assertEqual(t, "test-user", credentials.UserName)
			assertEqual(t, "test-password", credentials.Password)
			assertEqual(t, tt.companyIDs, credentials.CompanyIDs)
		})
	}
}

func TestLoadCredentialsRejectsMalformedYAML(t *testing.T) {
	credentialsFile := filepath.Join(t.TempDir(), "credentials.yaml")
	assertNoError(t, os.WriteFile(credentialsFile, []byte("username: [\n"), 0o600))

	if _, err := loadCredentials(credentialsFile); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}
