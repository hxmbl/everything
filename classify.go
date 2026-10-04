// Deciding whether a file is a secret or a binary, from its name and its head
// bytes. Pure functions: no Config, no I/O.

package main

import (
	"bytes"
	"path/filepath"
	"strings"
)

//
// SECRETS GUARD
//

var secretExtensions = map[string]bool{
	".pem": true, ".key": true, ".p12": true, ".pfx": true, ".jks": true,
	".keystore": true, ".jceks": true, ".kdbx": true, ".crt": true, ".cer": true,
	".der": true, ".csr": true, ".asc": true, ".gpg": true, ".pgp": true,
}

var secretDataExts = map[string]bool{
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true,
	".conf": true, ".cfg": true, ".txt": true, ".properties": true,
}

// sourceCodeExts are extensions of hand-written program text, and they are the
// only thing that excuses a file from the bare secret-word rule below. Those
// words name source files all day -- auth.go, authorization.go, tokenizer.go,
// private.go, aws-sdk.js -- and dropping one deletes the user's own code from
// the dump, which is the single thing this tool exists to avoid.
//
// The exemption is enumerated rather than inferred, so that an extension nobody
// listed keeps the strict reading: this list can only shrink the set of skipped
// files, never grow it. The omissions are deliberate. .md, .txt, .rst and .adoc
// are where a password or token gets pasted into prose; .sql, .sh, .html and
// .config can carry live credentials; the structured data formats are already
// secretDataExts. Those stay subject to the strict rule, as does anything with
// no extension at all. A new language belongs here when it starts hiding
// behind a secret word.
var sourceCodeExts = map[string]bool{
	// C family
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".cxx": true,
	".hh": true, ".hpp": true, ".hxx": true, ".m": true, ".mm": true,

	// Other compiled languages
	".go": true, ".rs": true, ".swift": true, ".zig": true, ".nim": true,
	".d": true, ".cr": true, ".vala": true, ".sol": true, ".v": true,
	".sv": true, ".s": true, ".asm": true, ".f90": true,

	// JVM and .NET
	".java": true, ".kt": true, ".kts": true, ".scala": true, ".groovy": true,
	".clj": true, ".cljs": true, ".cljc": true, ".cs": true, ".fs": true,
	".fsx": true, ".fsi": true, ".vb": true,

	// Scripted and dynamic
	".py": true, ".pyi": true, ".rb": true, ".rake": true, ".pl": true,
	".pm": true, ".php": true, ".lua": true, ".tcl": true, ".r": true,
	".jl": true, ".dart": true, ".ex": true, ".exs": true, ".erl": true,
	".hrl": true, ".el": true, ".lisp": true, ".elm": true,

	// Functional and academic
	".hs": true, ".lhs": true, ".ml": true, ".mli": true, ".pas": true,
	".ada": true, ".adb": true, ".ads": true,

	// Browser and frontend code. A component called Auth.vue is code; a token
	// pasted into one is the failure mode every .env file already gets caught
	// for.
	".js": true, ".mjs": true, ".cjs": true, ".jsx": true, ".ts": true,
	".tsx": true, ".mts": true, ".cts": true, ".vue": true, ".svelte": true,
	".astro": true, ".proto": true, ".graphql": true, ".gql": true,
}

var binaryMagics = [][]byte{
	{0x7f, 'E', 'L', 'F'},
	{'M', 'Z'},
	{'%', 'P', 'D', 'F'},
	{0x89, 'P', 'N', 'G'},
	{'P', 'K', 0x03, 0x04},
	{0x1f, 0x8b},
	{0x42, 0x5a},
	{0xfd, 0x37, 0x7a, 0x58, 0x5a},
	{0xfe, 0xed, 0xfa, 0xce},
	{0xfe, 0xed, 0xfa, 0xcf},
	{0xce, 0xfa, 0xed, 0xfe},
	{0xcf, 0xfa, 0xed, 0xfe},
}

// isSecretFilename checks if a filename matches known secret file patterns.
// This includes environment files, SSH keys, certificates, and credential files.
func isSecretFilename(name string) bool {
	lower := strings.ToLower(name)

	if strings.HasPrefix(lower, ".env") || strings.Contains(lower, ".env.") ||
		strings.HasSuffix(lower, ".env") {
		return true
	}
	if strings.HasPrefix(lower, "id_rsa") || strings.HasPrefix(lower, "id_ed25519") ||
		strings.HasPrefix(lower, "id_dsa") || strings.HasPrefix(lower, "id_ecdsa") {
		return true
	}
	switch lower {
	case ".htpasswd", ".netrc", ".npmrc", ".pypirc", ".git-credentials", "credentials":
		return true
	}
	if strings.HasPrefix(lower, "credentials.") || strings.HasPrefix(lower, "client_secret") ||
		strings.HasPrefix(lower, "client-secret") {
		return true
	}
	if (strings.Contains(lower, "service-account") || strings.Contains(lower, "service_account")) &&
		filepath.Ext(lower) == ".json" {
		return true
	}
	if lower == "secrets" || strings.HasPrefix(lower, "secrets.") {
		if lower == "secrets" || secretDataExts[filepath.Ext(lower)] {
			return true
		}
	}

	ext := filepath.Ext(lower)

	// Additional secret file patterns. The words are not evidence on their own:
	// auth.go and tokenizer.go are code, so the rule stands down for any
	// extension known to be source. See sourceCodeExts for why the exemption is
	// an explicit list rather than a guess about unknown extensions.
	if !sourceCodeExts[ext] &&
		(strings.HasPrefix(lower, "secret") || strings.HasPrefix(lower, "auth") ||
			strings.HasPrefix(lower, "token") || strings.HasPrefix(lower, "password") ||
			strings.HasPrefix(lower, "api_key") || strings.HasPrefix(lower, "api-key") ||
			strings.HasPrefix(lower, "private") || strings.HasPrefix(lower, "aws")) {
		return true
	}
	if strings.Contains(lower, "config") && (strings.Contains(lower, "secret") ||
		strings.Contains(lower, "credential") || strings.Contains(lower, "auth")) {
		return true
	}

	if secretExtensions[ext] {
		return true
	}

	return false
}

var pemBeginMarker = []byte("-----BEGIN")
var pemPrivateKeyMarker = []byte("PRIVATE KEY")

// hasPrivateKeyMarker checks if data contains a PEM private key block.
// It looks for "-----BEGIN" followed by "PRIVATE KEY" within the first 4KB.
func hasPrivateKeyMarker(data []byte) bool {
	n := len(data)
	if n > 4096 {
		n = 4096
	}
	window := bytes.TrimPrefix(data[:n], []byte{0xef, 0xbb, 0xbf})
	if len(window) < 10 {
		return false
	}
	return bytes.Contains(window, pemBeginMarker) && bytes.Contains(window, pemPrivateKeyMarker)
}

// hasMagic checks if the peek bytes start with the given magic sequence.
func hasMagic(peek, magic []byte) bool {
	if len(peek) < len(magic) {
		return false
	}
	for i, b := range magic {
		if peek[i] != b {
			return false
		}
	}
	return true
}

// isBinary checks if the file content appears to be binary based on magic bytes and control character density.
// Returns true if the content matches known binary signatures or has too many control characters.
func isBinary(peek []byte) bool {
	for _, m := range binaryMagics {
		if len(peek) >= len(m) && hasMagic(peek, m) {
			return true
		}
	}

	if len(peek) == 0 {
		return false
	}

	controlCount := 0
	for _, b := range peek {
		if b == 0 {
			return true
		}
		if b < 0x20 && b != 0x09 && b != 0x0a && b != 0x0d {
			controlCount++
		}
	}
	return float64(controlCount)/float64(len(peek)) > 0.10
}
