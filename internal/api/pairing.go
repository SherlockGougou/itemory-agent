package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Errors returned by the pairing flow.
var (
	ErrClaimExpired  = errors.New("pairing token expired")
	ErrClaimMismatch = errors.New("pairing token does not match")
	ErrClaimLocked   = errors.New("too many failed attempts, try again later")
	ErrClaimMissing  = errors.New("no pairing token configured")
)

// ClaimTTL is how long a pairing window stays usable once armed.
const ClaimTTL = 5 * time.Minute

// Token is one paired device (the secret itself is never persisted).
type Token struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
	Hash      string    `json:"hash"`
	// Platform 由配对时的客户端上报（ios / ipados / tvos / macos）。
	// 老版本 App 不传，因此这是可选字段——控制台在缺失时降级显示，
	// 不因为这个字段把配对流程卡住。
	Platform string `json:"platform,omitempty"`
}

type tokenFile struct {
	ServerID    string    `json:"serverId"`
	ClaimHash   string    `json:"claimHash,omitempty"`
	ClaimExpiry time.Time `json:"claimExpiry,omitempty"`
	Tokens      []Token   `json:"tokens"`
}

// TokenStore persists paired devices and the one-time claim token.
type TokenStore struct {
	mu          sync.Mutex
	path        string
	serverID    string
	claimCode   string
	claimHash   string
	claimExpiry time.Time
	claimFails  int
	lockUntil   time.Time
	tokens      map[string]Token
	lastPersist time.Time
}

// LoadTokens reads (or creates) the token file inside the data directory.
func LoadTokens(dir string) (*TokenStore, error) {
	store := &TokenStore{
		path:   filepath.Join(dir, "tokens.json"),
		tokens: map[string]Token{},
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		store.serverID = randomHex(8)
		return store, store.persistLocked()
	}
	var file tokenFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	if file.ServerID == "" {
		file.ServerID = randomHex(8)
	}
	store.serverID = file.ServerID
	store.claimHash = file.ClaimHash
	store.claimExpiry = file.ClaimExpiry
	for _, tok := range file.Tokens {
		store.tokens[tok.ID] = tok
	}
	return store, nil
}

// ServerID identifies this agent instance to the app.
func (t *TokenStore) ServerID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.serverID
}

// SetClaim arms a one-time pairing token.
func (t *TokenStore) SetClaim(code string, ttl time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.claimCode = code
	t.claimHash = hashSecret(code)
	t.claimExpiry = time.Now().Add(ttl)
	t.claimFails = 0
	t.lockUntil = time.Time{}
	_ = t.persistLocked()
}

// ClearClaim closes the pairing window immediately (idempotent).
func (t *TokenStore) ClearClaim() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.claimCode = ""
	t.claimHash = ""
	t.claimExpiry = time.Time{}
	t.claimFails = 0
	t.lockUntil = time.Time{}
	_ = t.persistLocked()
}

// ClaimArmed reports whether a claim token is currently usable.
func (t *TokenStore) ClaimArmed() bool {
	armed, _ := t.ClaimState()
	return armed
}

// ClaimState reports the pairing window and when it closes.
//
// A window armed by a previous process (its code lives only in memory) counts as
// closed: nothing can render it into a QR anymore.
func (t *TokenStore) ClaimState() (bool, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.claimCode == "" || t.claimHash == "" || !time.Now().Before(t.claimExpiry) {
		return false, time.Time{}
	}
	return true, t.claimExpiry
}

// ClaimCode returns the plaintext pairing code while the window is open.
//
// The code is kept in memory only: the dashboard renders it into the pairing
// QR without ever putting it in a URL, and the on-disk file keeps just the hash.
func (t *TokenStore) ClaimCode() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.claimCode == "" || !time.Now().Before(t.claimExpiry) {
		return "", false
	}
	return t.claimCode, true
}

// Redeem exchanges a claim token for a long-lived device token.
func (t *TokenStore) Redeem(code, deviceName, platform string) (Token, string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.claimHash == "" {
		return Token{}, "", ErrClaimMissing
	}
	if time.Now().After(t.claimExpiry) {
		return Token{}, "", ErrClaimExpired
	}
	if time.Now().Before(t.lockUntil) {
		return Token{}, "", ErrClaimLocked
	}
	if subtle.ConstantTimeCompare([]byte(hashSecret(code)), []byte(t.claimHash)) != 1 {
		t.claimFails++
		if t.claimFails >= 3 {
			t.lockUntil = time.Now().Add(60 * time.Second)
			t.claimFails = 0
		}
		return Token{}, "", ErrClaimMismatch
	}
	secret := randomHex(32)
	token := Token{
		ID:        randomHex(6),
		Name:      truncate(deviceName, 64),
		CreatedAt: time.Now(),
		LastSeen:  time.Now(),
		Hash:      hashSecret(secret),
		Platform:  normalizePlatform(platform),
	}
	t.tokens[token.ID] = token
	// The claim token is single-use.
	t.claimCode = ""
	t.claimHash = ""
	t.claimExpiry = time.Time{}
	if err := t.persistLocked(); err != nil {
		return Token{}, "", err
	}
	return token, secret, nil
}

// platformAliases 收敛客户端可能上报的平台写法。
// 认不出来就返回空串，而不是把任意字符串原样存下来——这个字段会被控制台
// 直接显示，不能由客户端决定它显示什么。
var platformAliases = map[string]string{
	"ios":      "ios",
	"iphone":   "ios",
	"iphoneos": "ios",
	"ipados":   "ipados",
	"ipad":     "ipados",
	"tvos":     "tvos",
	"appletv":  "tvos",
	"macos":    "macos",
	"mac":      "macos",
	"catalyst": "macos",
	"watchos":  "watchos",
	"watch":    "watchos",
	"visionos": "visionos",
	"vision":   "visionos",
}

func normalizePlatform(raw string) string {
	return platformAliases[strings.ToLower(strings.TrimSpace(raw))]
}

// Verify checks a bearer secret and refreshes last-seen (throttled persistence).
func (t *TokenStore) Verify(secret string) bool {
	if secret == "" {
		return false
	}
	hashed := hashSecret(secret)
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, tok := range t.tokens {
		if subtle.ConstantTimeCompare([]byte(hashed), []byte(tok.Hash)) == 1 {
			if time.Since(tok.LastSeen) > time.Minute {
				tok.LastSeen = time.Now()
				t.tokens[id] = tok
				if time.Since(t.lastPersist) > 5*time.Minute {
					_ = t.persistLocked()
				}
			}
			return true
		}
	}
	return false
}

// List returns all paired devices (hash included; callers must strip it).
func (t *TokenStore) List() []Token {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Token, 0, len(t.tokens))
	for _, tok := range t.tokens {
		out = append(out, tok)
	}
	return out
}

// Revoke removes one device token.
func (t *TokenStore) Revoke(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.tokens[id]; !ok {
		return fmt.Errorf("token %s not found", id)
	}
	delete(t.tokens, id)
	return t.persistLocked()
}

// Count returns how many devices are paired.
func (t *TokenStore) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.tokens)
}

func (t *TokenStore) persistLocked() error {
	file := tokenFile{
		ServerID:    t.serverID,
		ClaimHash:   t.claimHash,
		ClaimExpiry: t.claimExpiry,
		Tokens:      make([]Token, 0, len(t.tokens)),
	}
	for _, tok := range t.tokens {
		file.Tokens = append(file.Tokens, tok)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, t.path); err != nil {
		return err
	}
	t.lastPersist = time.Now()
	return nil
}

// RandomCode builds a numeric pairing code (e.g. 6 digits).
func RandomCode(digits int) string {
	out := make([]byte, digits)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			out[i] = '0'
			continue
		}
		out[i] = byte('0' + n.Int64())
	}
	return string(out)
}

func randomHex(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte("itemory-agent|" + secret))
	return hex.EncodeToString(sum[:])
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
