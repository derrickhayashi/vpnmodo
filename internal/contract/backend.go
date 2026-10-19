package contract

import (
	"reflect"
	"strings"
)

type Field struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
	Format   string `json:"format,omitempty"`
}

type Capabilities struct {
	MFA               bool `json:"mfa"`
	AutomaticRecovery bool `json:"automatic_recovery"`
	RequireFailClosed bool `json:"require_fail_closed"`
}

type BackendDescriptor struct {
	Type             BackendType  `json:"type"`
	ConfigFields     []Field      `json:"config_fields"`
	CredentialFields []Field      `json:"credential_fields"`
	Capabilities     Capabilities `json:"capabilities"`
}

// DescribeBackend derives field names/types from the same structs used for
// validation. Capabilities express requirements, not a claim of implementation.
func DescribeBackend(kind BackendType) (BackendDescriptor, error) {
	d := BackendDescriptor{Type: kind, CredentialFields: fields(reflect.TypeFor[Credentials]())}
	d.Capabilities.RequireFailClosed = true
	switch kind {
	case L2TP:
		d.ConfigFields = fields(reflect.TypeFor[L2TPConfig]())
		d.Capabilities.AutomaticRecovery = true
	case XAuth:
		d.ConfigFields = fields(reflect.TypeFor[XAuthConfig]())
		d.Capabilities.MFA = true
	default:
		return BackendDescriptor{}, ErrBackend
	}
	return d, nil
}

func fields(t reflect.Type) []Field {
	var result []Field
	for field := range t.Fields() {
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		kind := "string"
		base := field.Type
		if base.Kind() == reflect.Pointer {
			base = base.Elem()
		}
		if base.Kind() == reflect.Slice {
			kind = "array"
		} else if base.Kind() == reflect.Bool {
			kind = "boolean"
		}
		result = append(result, Field{Name: name, Type: kind, Required: field.Tag.Get("required") == "true", Secret: field.Tag.Get("secret") == "true", Format: field.Tag.Get("format")})
	}
	return result
}
