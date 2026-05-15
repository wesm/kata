package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
	_ "time/tzdata" // embed IANA tz database so LoadLocation works without system tzdata
)

// ErrUnknownKey is returned when a key is not present in the registry.
var ErrUnknownKey = errors.New("unknown metadata key")

// ErrInvalidValue is returned when a value fails type-specific validation.
var ErrInvalidValue = errors.New("invalid value")

// reULID matches a 26-character Crockford base32 ULID.
var reULID = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// Validate checks that raw is a valid value for key in the given registry.
// A JSON null value is always accepted and signals "clear this key".
// Returns ErrUnknownKey when key is absent from the registry.
func Validate(registry map[string]Entry, key string, raw json.RawMessage) error {
	entry, ok := registry[key]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	if string(raw) == "null" {
		return nil
	}
	switch entry.Type {
	case TypeDate:
		return validateDate(raw)
	case TypeBool:
		return validateBool(raw)
	case TypeEnum:
		return validateEnum(raw, entry.Enum)
	case TypeString:
		return validateString(raw)
	case TypeInt:
		return validateInt(raw)
	case TypeChecklist:
		return validateChecklist(raw)
	case TypeTimezoneIANA:
		return validateTimezoneIANA(raw)
	default:
		return fmt.Errorf("no validator for type %d", entry.Type)
	}
}

func validateDate(raw json.RawMessage) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("date must be a JSON string: %w", err)
	}
	_, err := time.Parse("2006-01-02", s)
	if err != nil {
		return fmt.Errorf("date must match YYYY-MM-DD: %w", err)
	}
	return nil
}

func validateBool(raw json.RawMessage) error {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return fmt.Errorf("value must be a JSON boolean: %w", err)
	}
	return nil
}

func validateEnum(raw json.RawMessage, allowed []string) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("enum value must be a JSON string: %w", err)
	}
	for _, v := range allowed {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("value %q is not one of %v", s, allowed)
}

func validateString(raw json.RawMessage) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("value must be a JSON string: %w", err)
	}
	return nil
}

func validateInt(raw json.RawMessage) error {
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return fmt.Errorf("value must be a JSON integer: %w", err)
	}
	return nil
}

// checklistItem is one entry in a TypeChecklist array.
type checklistItem struct {
	ID   string  `json:"id"`
	Text *string `json:"text"`
	Done *bool   `json:"done"`
}

func validateChecklist(raw json.RawMessage) error {
	var items []checklistItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return fmt.Errorf("%w: checklist must be an array of items: %v", ErrInvalidValue, err)
	}
	for i, it := range items {
		if !reULID.MatchString(it.ID) {
			return fmt.Errorf("%w: item[%d].id must be a 26-char ULID, got %q",
				ErrInvalidValue, i, it.ID)
		}
		if it.Text == nil {
			return fmt.Errorf("%w: item[%d].text required", ErrInvalidValue, i)
		}
		if it.Done == nil {
			return fmt.Errorf("%w: item[%d].done required", ErrInvalidValue, i)
		}
	}
	return nil
}

func validateTimezoneIANA(raw json.RawMessage) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("timezone must be a JSON string: %w", err)
	}
	if _, err := time.LoadLocation(s); err != nil {
		return fmt.Errorf("timezone %q is not a valid IANA name: %w", s, err)
	}
	return nil
}
