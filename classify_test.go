// Tests for secret and binary classification.

package main

import (
	"strings"
	"testing"
)

func TestIsSecretFilename(t *testing.T) {
	cases := []struct {
		name string
		skip bool
	}{
		{"credentials", true},
		{"credentials.json", true},
		{"credentials.yml", true},
		{"credentials.txt", true},
		{"CREDENTIALS", true},
		{"prod.env", true},
		{"config.env", true},
		{"staging.env.local", true},
		{".env", true},
		{".env.local", true},
		{".ENVIRONMENT", true},
		{"environment", false},
		{".npmrc", true},
		{".pypirc", true},
		{".git-credentials", true},
		{"client_secret.json", true},
		{"client_secret_1234.apps.googleusercontent.com.json", true},
		{"service-account.json", true},
		{"myproj-abc123-service-account.json", true},
		{"service_account_key.JSON", true},
		{"secrets.yaml", true},
		{"secrets", true},
		{"secrets.txt", true},
		{"id_rsa", true},
		{"id_rsa.bak", true},
		{"id_ed25519_old", true},
		{"server.key", true},
		{"cert.pem", true},
		{"backup.p12", true},
		{"keystore.jks", true},
		{"store.keystore", true},
		{"vault.kdbx", true},
		{".netrc", true},
		{".htpasswd", true},

		{"main.go", false},
		{"notes.txt", false},
		{"env.example", false},
		{"provenance.go", false},
		{"secrets.go", false},
		{"secrets_test.py", false},
		{"credentials.md", true},
		{"tokens.go", false},
		{"readme.md", false},
	}
	for _, c := range cases {
		if got := isSecretFilename(c.name); got != c.skip {
			t.Errorf("isSecretFilename(%q) = %v, want %v", c.name, got, c.skip)
		}
	}
}

// TestIsSecretFilenameSecretWordPrefixes covers the bare-word rule on its own.
// Those words ("auth", "token", "password", "private", "aws", "secret",
// "api_key") are ordinary filename prefixes, so the rule has to stand down for
// source code and hold everywhere else: no extension, a data format, a
// credential format, or an extension nobody has an opinion about.
func TestIsSecretFilenameSecretWordPrefixes(t *testing.T) {
	cases := []struct {
		name string
		skip bool
	}{
		// A secret word plus a source extension is the user's own code. Every
		// one of these was dropped from real dumps before the rule learned to
		// look at the extension.
		{"tokenizer.go", false},
		{"authorization.go", false},
		{"authentication.go", false},
		{"auth.go", false},
		{"auth.ts", false},
		{"auth.tsx", false},
		{"auth.py", false},
		{"author.rs", false},
		{"authorizer.rb", false},
		{"authentic.go", false},
		{"authz.go", false},
		{"awsome.go", false},
		{"aws_test.go", false},
		{"aws-sdk.js", false},
		{"private.go", false},
		{"privately.go", false},
		{"secrets.go", false},
		{"secrets.js", false},
		{"secretsmanager.py", false},
		{"secrets_test.py", false},
		{"passwordless.go", false},
		{"passwords.py", false},
		{"tokenize.rs", false},
		{"tokens.go", false},
		{"api_keys.go", false},

		// No extension at all. There is no such thing as a source file called
		// "token", and these are also the directory names this rule prunes.
		{"token", true},
		{"tokens", true},
		{"auth", true},
		{"secrets", true},
		{"secret", true},
		{"password", true},
		{"passwords", true},
		{"private", true},
		{"aws", true},
		{"api_key", true},
		{"api-key", true},

		// Data and config formats: a secret word in one of these is a stored
		// value, not an identifier.
		{"token.json", true},
		{"tokens.yml", true},
		{"auth.conf", true},
		{"private.toml", true},
		{"aws.properties", true},
		{"api_key.ini", true},
		{"api-key.json", true},
		{"passwords.json", true},
		{"passwords.txt", true},
		{"secrets.yml", true},
		{"secrets.json", true},

		// Formats that can hold live credentials but are not in the data list.
		// These fail closed, which is the point of the exemption being a list.
		{"auth.sh", true},
		{"auth.sql", true},
		{"auth.html", true},
		{"auth.xml", true},
		{"auth.config", true},
		{"token.md", true},
		{"password.md", true},
		{"secrets.md", true},
		{"authentic.md", true},
		{"awsome.md", true},
		{"aws.credentials", true},
		{"credentials.txt", true},
		{"credentials.md", true},

		// Key and certificate extensions, which secretExtensions owns. The
		// bare-word rule must not become a second, weaker path to them.
		{"private.key", true},
		{"auth.pem", true},
		{"secret.asc", true},
		{"tokens.jks", true},
		{"auth.pfx", true},
		{"auth.cer", true},
		{"aws.crt", true},
		{"aws.der", true},
		{"auth.csr", true},
		{"auth.gpg", true},
	}
	for _, c := range cases {
		if got := isSecretFilename(c.name); got != c.skip {
			t.Errorf("isSecretFilename(%q) = %v, want %v", c.name, got, c.skip)
		}
	}
}

// TestSourceCodeExtsCannotOverlapSecretExts keeps the source exemption from
// eating a format the secret side already claims. An extension in both sets
// would make the same file secret in one rule and harmless in the next, which
// is the kind of drift the ignore lists are built to avoid.
func TestSourceCodeExtsCannotOverlapSecretExts(t *testing.T) {
	for ext := range sourceCodeExts {
		if !strings.HasPrefix(ext, ".") || len(ext) < 2 {
			t.Errorf("sourceCodeExts entry %q must be an extension with a leading dot", ext)
		}
		if strings.ContainsAny(ext, `/\ `) {
			t.Errorf("sourceCodeExts entry %q must be a bare extension", ext)
		}
		if secretDataExts[ext] {
			t.Errorf("sourceCodeExts entry %q is also a secretDataExts value", ext)
		}
		if secretExtensions[ext] {
			t.Errorf("sourceCodeExts entry %q is also a secretExtensions value", ext)
		}
	}
}

func TestHasPrivateKeyMarker(t *testing.T) {
	key := "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----"

	cases := []struct {
		name string
		data string
		want bool
	}{
		{"plain key at byte 0", key, true},
		{"leading newline", "\n" + key, true},
		{"leading spaces", "   \n\t" + key, true},
		{"utf8 bom", "\xef\xbb\xbf" + key, true},
		{"header past offset 128", strings.Repeat("x", 200) + "\n" + key, true},
		{"json wrapped", `{"key":"` + key + `"}`, true},
		{"openssh", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaA==", true},
		{"pkcs8", "-----BEGIN PRIVATE KEY-----\nMIIEvQ==", true},
		{"pgp", "-----BEGIN PGP PRIVATE KEY BLOCK-----\nabc=", true},
		{"prose mention only", "// this file parses PRIVATE KEY headers", false},
		{"begin without private key", "-----BEGIN CERTIFICATE-----\nabc=", false},
		{"empty", "", false},
		{"tiny", "short", false},
		{"marker beyond window", strings.Repeat("y", 5000) + key, false},
	}
	for _, c := range cases {
		if got := hasPrivateKeyMarker([]byte(c.data)); got != c.want {
			t.Errorf("hasPrivateKeyMarker(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsBinary(t *testing.T) {
	if isBinary([]byte("plain text file\n")) {
		t.Error("text flagged as binary")
	}
	if !isBinary([]byte{0x7f, 'E', 'L', 'F', 0x00}) {
		t.Error("ELF not flagged")
	}
	if !isBinary([]byte("has\x00nul")) {
		t.Error("NUL not flagged")
	}
	if isBinary([]byte{}) {
		t.Error("empty flagged as binary")
	}
	// Test magic bytes
	if !isBinary([]byte{'P', 'K', 0x03, 0x04}) { // ZIP
		t.Error("ZIP not flagged")
	}
	if !isBinary([]byte{0x89, 'P', 'N', 'G'}) { // PNG
		t.Error("PNG not flagged")
	}
	// Test control character density
	controlHeavy := make([]byte, 100)
	for i := range controlHeavy {
		controlHeavy[i] = 0x01
	}
	if !isBinary(controlHeavy) {
		t.Error("control-heavy data not flagged")
	}
}

func TestHasMagic(t *testing.T) {
	// Test exact match
	if !hasMagic([]byte{'P', 'K', 0x03, 0x04, 0x05}, []byte{'P', 'K', 0x03, 0x04}) {
		t.Error("exact magic match failed")
	}
	// Test too short
	if hasMagic([]byte{'P', 'K'}, []byte{'P', 'K', 0x03, 0x04}) {
		t.Error("should fail on too short data")
	}
	// Test mismatch
	if hasMagic([]byte{'P', 'K', 0x03, 0x05}, []byte{'P', 'K', 0x03, 0x04}) {
		t.Error("should fail on mismatch")
	}
}
