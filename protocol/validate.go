package protocol

import (
	"fmt"
	"regexp"
	"strings"
)

var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$`)

// Validate checks that CorrelationID is set and that every field is a
// namespaced reference.
func (c Correlation) Validate() error {
	if err := ValidateRef(c.CorrelationID); err != nil {
		return fmt.Errorf("protocol: correlationid: %w", err)
	}
	if err := validateOptionalRef("causationid", c.CausationID); err != nil {
		return err
	}
	return validateOptionalRef("sessionid", c.SessionID)
}

// ValidateRef checks that ref has the form "namespace:value", with a
// lower-case dotted namespace, a non-empty value without whitespace, and at
// most 255 bytes in total.
func ValidateRef(ref string) error {
	namespace, value, ok := strings.Cut(ref, ":")
	if !ok || len(ref) > 255 || !namespacePattern.MatchString(namespace) || value == "" || strings.ContainsAny(value, " \t\r\n") {
		return fmt.Errorf("protocol: malformed reference %q", ref)
	}
	return nil
}

func validateOptionalRef(label, value string) error {
	if value == "" {
		return nil
	}
	if err := ValidateRef(value); err != nil {
		return fmt.Errorf("protocol: %s: %w", label, err)
	}
	return nil
}
