// Package secret encrypts values at rest with AES-256-GCM.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const prefix = "enc:v1:"

var aead cipher.AEAD

// Init sets the process-wide key. It must run before any Secret is read or
// written.
func Init(key []byte) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	aead = g
	return nil
}

func Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if aead == nil {
		return "", errors.New("secret: not initialised")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

func Decrypt(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !strings.HasPrefix(stored, prefix) {
		return "", errors.New("secret: value is not encrypted")
	}
	if aead == nil {
		return "", errors.New("secret: not initialised")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, prefix))
	if err != nil {
		return "", err
	}
	ns := aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("secret: ciphertext too short")
	}
	plain, err := aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("secret: %w (was MASTER_KEY changed?)", err)
	}
	return string(plain), nil
}

// String is a column that is encrypted in the database and plain in memory.
// It is never serialised to JSON by the models that use it (json:"-").
type String string

func (s String) Value() (driver.Value, error) {
	return Encrypt(string(s))
}

func (s *String) Scan(v any) error {
	var stored string
	switch t := v.(type) {
	case nil:
		*s = ""
		return nil
	case string:
		stored = t
	case []byte:
		stored = string(t)
	default:
		return fmt.Errorf("secret: cannot scan %T", v)
	}
	plain, err := Decrypt(stored)
	if err != nil {
		return err
	}
	*s = String(plain)
	return nil
}

func (String) GormDataType() string { return "text" }
