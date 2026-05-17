package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// StringArray is stored as JSON in the database (compatible with both SQLite and PostgreSQL).
type StringArray []string

func (a *StringArray) Scan(src interface{}) error {
	if src == nil {
		*a = StringArray{}
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		return fmt.Errorf("unsupported type for StringArray: %T", src)
	}
	var result []string
	if err := json.Unmarshal(b, &result); err != nil {
		*a = StringArray{}
		return nil
	}
	*a = result
	return nil
}

func (a StringArray) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal([]string(a))
	if err != nil {
		return "[]", nil
	}
	return string(b), nil
}

// JSONRaw is a []byte slice stored as TEXT in the database.
// Unlike json.RawMessage, it implements sql.Scanner so SQLite TEXT columns
// (returned as Go string by the driver) are handled correctly.
type JSONRaw []byte

func (j *JSONRaw) Scan(src interface{}) error {
	if src == nil {
		*j = JSONRaw("null")
		return nil
	}
	switch v := src.(type) {
	case string:
		*j = JSONRaw(v)
	case []byte:
		*j = append(JSONRaw(nil), v...)
	default:
		return fmt.Errorf("unsupported type for JSONRaw: %T", src)
	}
	return nil
}

func (j JSONRaw) Value() (driver.Value, error) {
	if len(j) == 0 {
		return "null", nil
	}
	return string(j), nil
}

func (j JSONRaw) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return []byte(j), nil
}

func (j *JSONRaw) UnmarshalJSON(data []byte) error {
	*j = append(JSONRaw(nil), data...)
	return nil
}
