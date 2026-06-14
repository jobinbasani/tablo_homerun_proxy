package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
	_ "modernc.org/sqlite"
)

var ErrAdminPasswordNotSet = errors.New("admin password is not set")
var ErrTabloCredentialsNotFound = errors.New("tablo credentials are not configured")

type Store struct {
	db *sql.DB
}

type ConfigEnvelope struct {
	Config         config.Config `json:"config"`
	RestartPending bool          `json:"restartPending"`
	UpdatedAt      time.Time     `json:"updatedAt"`
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS app_config (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	config_json TEXT NOT NULL,
	restart_pending INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS admin_auth (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	password_hash TEXT NOT NULL,
	session_token_hash TEXT,
	session_expires_at TEXT,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tablo_credentials (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	encrypted_json BLOB NOT NULL,
	updated_at TEXT NOT NULL
);
`)
	return err
}

func (s *Store) Init(ctx context.Context, cfg config.Config) error {
	exists, err := s.configExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		cfg.AdminPassword = ""
		cfg.UserPass = ""
		if err := s.SaveConfig(ctx, cfg, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) LoadConfig(ctx context.Context) (config.Config, bool, error) {
	var raw string
	var restartPending int
	err := s.db.QueryRowContext(ctx, `SELECT config_json, restart_pending FROM app_config WHERE id = 1`).Scan(&raw, &restartPending)
	if err != nil {
		return config.Config{}, false, err
	}
	var cfg config.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return config.Config{}, false, err
	}
	return config.ApplyDerived(cfg), restartPending == 1, nil
}

func (s *Store) SaveConfig(ctx context.Context, cfg config.Config, restartPending bool) error {
	cfg = config.ApplyDerived(cfg)
	cfg.AdminPassword = ""
	cfg.UserPass = ""
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	pending := 0
	if restartPending {
		pending = 1
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO app_config (id, config_json, restart_pending, updated_at)
VALUES (1, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	config_json = excluded.config_json,
	restart_pending = excluded.restart_pending,
	updated_at = excluded.updated_at
`, string(data), pending, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) SaveTabloCredentials(ctx context.Context, encrypted []byte) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO tablo_credentials (id, encrypted_json, updated_at)
VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	encrypted_json = excluded.encrypted_json,
	updated_at = excluded.updated_at
`, encrypted, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) LoadTabloCredentials(ctx context.Context) ([]byte, error) {
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `SELECT encrypted_json FROM tablo_credentials WHERE id = 1`).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTabloCredentialsNotFound
	}
	if err != nil {
		return nil, err
	}
	return encrypted, nil
}

func (s *Store) HasTabloCredentials(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tablo_credentials WHERE id = 1`).Scan(&count)
	return count > 0, err
}

func (s *Store) SetAdminPassword(ctx context.Context, password string) error {
	if password == "" {
		return ErrAdminPasswordNotSet
	}
	hash, err := hashSecret(password)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO admin_auth (id, password_hash, updated_at)
VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	password_hash = excluded.password_hash,
	updated_at = excluded.updated_at
`, hash, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) HasAdminPassword(ctx context.Context) (bool, error) {
	return s.hasAdminPassword(ctx)
}

func (s *Store) CheckAdminPassword(ctx context.Context, password string) (bool, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM admin_auth WHERE id = 1`).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrAdminPasswordNotSet
	}
	if err != nil {
		return false, err
	}
	return verifySecret(hash, password), nil
}

func (s *Store) CreateSession(ctx context.Context) (string, time.Time, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	hash := sha256.Sum256([]byte(token))
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	_, err = s.db.ExecContext(ctx, `UPDATE admin_auth SET session_token_hash = ?, session_expires_at = ?, updated_at = ? WHERE id = 1`,
		base64.RawURLEncoding.EncodeToString(hash[:]), expiresAt.Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func (s *Store) CheckSession(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	var storedHash, expiresRaw sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT session_token_hash, session_expires_at FROM admin_auth WHERE id = 1`).Scan(&storedHash, &expiresRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !storedHash.Valid || !expiresRaw.Valid {
		return false, nil
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresRaw.String)
	if err != nil || time.Now().UTC().After(expiresAt) {
		return false, nil
	}
	hash := sha256.Sum256([]byte(token))
	return storedHash.String == base64.RawURLEncoding.EncodeToString(hash[:]), nil
}

func (s *Store) ClearSession(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin_auth SET session_token_hash = NULL, session_expires_at = NULL, updated_at = ? WHERE id = 1`, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) configExists(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_config WHERE id = 1`).Scan(&count)
	return count > 0, err
}

func (s *Store) hasAdminPassword(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_auth WHERE id = 1`).Scan(&count)
	return count > 0, err
}

func hashSecret(secret string) (string, error) {
	salt, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append(salt, []byte(secret)...))
	return base64.RawURLEncoding.EncodeToString(salt) + ":" + base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func verifySecret(encoded, secret string) bool {
	saltPart, hashPart, ok := strings.Cut(encoded, ":")
	if !ok {
		return false
	}
	salt, err := base64.RawURLEncoding.DecodeString(saltPart)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(append(salt, []byte(secret)...))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == hashPart
}

func randomToken(size int) (string, error) {
	data, err := randomBytes(size)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func randomBytes(size int) ([]byte, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return nil, err
	}
	return data, nil
}
