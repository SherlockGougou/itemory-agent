package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	adminSessionCookie = "itemory_admin_session"
	adminSessionTTL    = 12 * time.Hour
	passwordIterations = 210000
)

var (
	errAdminAlreadyConfigured = errors.New("administrator is already configured")
)

type adminFile struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
}

type adminStore struct {
	mu       sync.RWMutex
	path     string
	username string
	password string
}

type adminSession struct {
	expiresAt time.Time
}

func loadAdminStore(dir string) (*adminStore, error) {
	store := &adminStore{path: filepath.Join(dir, "admin.json")}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	var file adminFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	store.username = file.Username
	store.password = file.PasswordHash
	return store, nil
}

func (s *adminStore) configured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.username != "" && s.password != ""
}

func (s *adminStore) usernameValue() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.username
}

func (s *adminStore) setup(username, password string) error {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(username) > 64 || strings.ContainsAny(username, "\r\n") {
		return errors.New("username must be 3-64 characters")
	}
	if len(password) < 12 || len(password) > 256 {
		return errors.New("password must be 12-256 characters")
	}

	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.username != "" || s.password != "" {
		return errAdminAlreadyConfigured
	}
	s.username = username
	s.password = hash
	return s.persistLocked()
}

func (s *adminStore) verify(username, password string) bool {
	s.mu.RLock()
	storedUsername, storedPassword := s.username, s.password
	s.mu.RUnlock()
	if storedUsername == "" || storedPassword == "" || username != storedUsername {
		return false
	}
	return verifyPassword(storedPassword, password)
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	derived := pbkdf2SHA256([]byte(password), salt, passwordIterations, 32)
	encoding := base64.RawStdEncoding
	return "pbkdf2-sha256$" + strconv.Itoa(passwordIterations) + "$" + encoding.EncodeToString(salt) + "$" + encoding.EncodeToString(derived), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 100000 || iterations > 1000000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 16 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(expected) != 32 {
		return false
	}
	actual := pbkdf2SHA256([]byte(password), salt, iterations, len(expected))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// pbkdf2SHA256 derives a password key using RFC 8018 PBKDF2 with HMAC-SHA256.
func pbkdf2SHA256(password, salt []byte, iterations, keyLength int) []byte {
	key := make([]byte, 0, keyLength)
	for block := 1; len(key) < keyLength; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		value := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range value {
				value[j] ^= u[j]
			}
		}
		key = append(key, value...)
	}
	return key[:keyLength]
}

func (s *adminStore) persistLocked() error {
	data, err := json.MarshalIndent(adminFile{Username: s.username, PasswordHash: s.password}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func newAdminSession() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return hex.EncodeToString(secret), nil
}

func (s *Server) setAdminSession(w http.ResponseWriter) error {
	secret, err := newAdminSession()
	if err != nil {
		return err
	}
	s.sessionsMu.Lock()
	s.sessions[secret] = adminSession{expiresAt: time.Now().Add(adminSessionTTL)}
	s.sessionsMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    secret,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(adminSessionTTL.Seconds()),
	})
	return nil
}

func (s *Server) clearAdminSession(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminSessionCookie); err == nil {
		s.sessionsMu.Lock()
		delete(s.sessions, cookie.Value)
		s.sessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func (s *Server) hasAdminSession(r *http.Request) bool {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	session, ok := s.sessions[cookie.Value]
	if !ok {
		return false
	}
	if !time.Now().Before(session.expiresAt) {
		delete(s.sessions, cookie.Value)
		return false
	}
	return true
}
