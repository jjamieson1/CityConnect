// Package domain holds the GORM models and the shared column types they use.
// Models carry no business logic beyond persistence concerns; behaviour lives
// in the service packages under internal/<area>.
package domain

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Base is embedded by every persisted model. IDs are UUIDs generated in the
// application rather than by the database, so a caller can know an entity's id
// before the write lands and so ids stay stable across environments.
type Base struct {
	ID        string         `gorm:"type:char(36);primaryKey" json:"id"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate assigns an id when the caller has not supplied one.
func (b *Base) BeforeCreate(*gorm.DB) error {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	return nil
}

// NewID returns a fresh entity identifier.
func NewID() string { return uuid.NewString() }

// JSONMap is a free-form object column. Stored as text so the same models work
// against MariaDB in production and SQLite in tests.
type JSONMap map[string]any

// Value implements driver.Valuer.
func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// Scan implements sql.Scanner.
func (m *JSONMap) Scan(src any) error {
	if src == nil {
		*m = JSONMap{}
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("domain: cannot scan %T into JSONMap", src)
	}
	if len(b) == 0 {
		*m = JSONMap{}
		return nil
	}
	return json.Unmarshal(b, m)
}

// String returns a key as a string, or "" when absent or of another type.
func (m JSONMap) String(key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// StringList is a slice-of-strings column, stored as a JSON array.
type StringList []string

// Value implements driver.Valuer.
func (l StringList) Value() (driver.Value, error) {
	if l == nil {
		return "[]", nil
	}
	b, err := json.Marshal(l)
	return string(b), err
}

// Scan implements sql.Scanner.
func (l *StringList) Scan(src any) error {
	if src == nil {
		*l = nil
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("domain: cannot scan %T into StringList", src)
	}
	if len(b) == 0 {
		*l = nil
		return nil
	}
	return json.Unmarshal(b, l)
}

// Contains reports whether the list holds the given value.
func (l StringList) Contains(v string) bool {
	for _, item := range l {
		if item == v {
			return true
		}
	}
	return false
}

// Normalized lower-cases and trims every entry, dropping blanks.
func (l StringList) Normalized() StringList {
	out := make(StringList, 0, len(l))
	for _, item := range l {
		if s := strings.ToLower(strings.TrimSpace(item)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ErrInvalidEnum is returned when a persisted enum column holds an
// unrecognised value.
var ErrInvalidEnum = errors.New("domain: invalid enum value")
