package atryum

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/validmind/atryum/internal/config"
)

const operatorTokenFileName = "atryum-operator-token"

// operatorTokenSource says where the operator token came from, which decides
// whether it is safe to print.
type operatorTokenSource int

const (
	operatorTokenFromConfig operatorTokenSource = iota
	operatorTokenFromFile
	operatorTokenGenerated
)

// resolveOperatorToken returns the operator token for a deployment with no
// admin-enabled [[auth]] block: the configured one (operator_token or
// ATRYUM_OPERATOR_TOKEN), else the one kept in the token file, else a new
// random token written to that file with mode 0600.
func resolveOperatorToken(server config.ServerConfig) (string, operatorTokenSource, string, error) {
	if token := strings.TrimSpace(server.OperatorToken); token != "" {
		return token, operatorTokenFromConfig, "", nil
	}
	path := operatorTokenPath(server)
	raw, err := os.ReadFile(path)
	if err == nil {
		if token := strings.TrimSpace(string(raw)); token != "" {
			if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm()&0o077 != 0 {
				log.Printf("WARNING: operator token file %s is readable by other users (mode %o); run chmod 600 on it", path, info.Mode().Perm())
			}
			return token, operatorTokenFromFile, path, nil
		}
		return "", 0, path, fmt.Errorf("operator token file %s is empty; delete it to generate a new token", path)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", 0, path, fmt.Errorf("read operator token file: %w", err)
	}
	token, err := newOperatorToken()
	if err != nil {
		return "", 0, path, err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", 0, path, fmt.Errorf("create operator token directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, path, fmt.Errorf("write operator token file: %w", err)
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		return "", 0, path, fmt.Errorf("write operator token file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", 0, path, fmt.Errorf("write operator token file: %w", err)
	}
	return token, operatorTokenGenerated, path, nil
}

// operatorTokenPath is operator_token_file, else atryum-operator-token beside
// the SQLite database, else in the working directory.
func operatorTokenPath(server config.ServerConfig) string {
	if path := strings.TrimSpace(server.OperatorTokenFile); path != "" {
		return path
	}
	if strings.TrimSpace(server.DatabaseURL) == "" && strings.TrimSpace(server.DatabasePath) != "" {
		return filepath.Join(filepath.Dir(server.DatabasePath), operatorTokenFileName)
	}
	return operatorTokenFileName
}

func newOperatorToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate operator token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// operatorBaseURL is public_base_url, else http://localhost on the listen port.
func operatorBaseURL(server config.ServerConfig) string {
	if base := strings.TrimRight(strings.TrimSpace(server.PublicBaseURL), "/"); base != "" {
		return base
	}
	port := "8080"
	if _, p, err := net.SplitHostPort(server.ListenAddr); err == nil && p != "" {
		port = p
	}
	return "http://localhost:" + port
}

// operatorSignInURL is the UI link that signs a browser in with token.
func operatorSignInURL(server config.ServerConfig, token string) string {
	return operatorBaseURL(server) + "/ui/?token=" + token
}

// logOperatorTokenNotice tells the operator how to sign in. A token the
// operator configured is never printed; a generated or stored one is, once,
// as a sign-in link, because nobody else knows it yet.
func logOperatorTokenNotice(server config.ServerConfig, token string, source operatorTokenSource, path string) {
	switch source {
	case operatorTokenFromConfig:
		log.Printf("operator API requires the configured operator token (no admin-enabled [[auth]] block); open %s/ui/ and enter it, or send it as Authorization: Bearer", operatorBaseURL(server))
	default:
		verb := "loaded from"
		if source == operatorTokenGenerated {
			verb = "generated and saved to"
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		log.Printf("operator API requires the operator token (no admin-enabled [[auth]] block); token %s %s", verb, path)
		log.Printf("sign in to the UI at: %s", operatorSignInURL(server, token))
	}
}
