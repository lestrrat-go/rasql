package schema

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// NativeTypeKind identifies the native type identity preserved from a server.
type NativeTypeKind string

const (
	NativeBuiltin NativeTypeKind = "builtin"
	NativeDomain  NativeTypeKind = "domain"
	NativeEnum    NativeTypeKind = "enum"
	NativeSet     NativeTypeKind = "set"
	NativeArray   NativeTypeKind = "array"
	NativeOther   NativeTypeKind = "other"
)

// NativeTypeDef records a server-native type independently of its portable
// ColumnType family.
type NativeTypeDef struct {
	Dialect   string         `json:"Dialect"`
	Schema    string         `json:"Schema,omitempty"`
	Name      string         `json:"Name"`
	Kind      NativeTypeKind `json:"Kind"`
	Arguments []string       `json:"Arguments,omitempty"`
	Element   *NativeTypeDef `json:"Element,omitempty"`
}

func (n NativeTypeDef) MarshalJSON() ([]byte, error) {
	type nativeWire struct {
		Dialect   string         `json:"Dialect"`
		Schema    string         `json:"Schema,omitempty"`
		Name      string         `json:"Name"`
		Kind      NativeTypeKind `json:"Kind"`
		Arguments *[]string      `json:"Arguments,omitempty"`
		Element   *NativeTypeDef `json:"Element,omitempty"`
	}
	var arguments *[]string
	if n.Arguments != nil {
		copyOfArguments := append([]string(nil), n.Arguments...)
		arguments = &copyOfArguments
	}
	return json.Marshal(nativeWire{Dialect: n.Dialect, Schema: n.Schema, Name: n.Name, Kind: n.Kind, Arguments: arguments, Element: n.Element})
}

func (n *NativeTypeDef) UnmarshalJSON(data []byte) error {
	type nativeWire struct {
		Dialect   string         `json:"Dialect"`
		Schema    string         `json:"Schema"`
		Name      string         `json:"Name"`
		Kind      NativeTypeKind `json:"Kind"`
		Arguments *[]string      `json:"Arguments"`
		Element   *NativeTypeDef `json:"Element"`
	}
	var wire nativeWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	n.Dialect, n.Schema, n.Name, n.Kind, n.Element = wire.Dialect, wire.Schema, wire.Name, wire.Kind, wire.Element
	if wire.Arguments == nil {
		n.Arguments = nil
		return nil
	}
	n.Arguments = make([]string, len(*wire.Arguments))
	copy(n.Arguments, *wire.Arguments)
	return nil
}

// OpaqueType represents a native type with no portable rasql equivalent.
type OpaqueType struct{}

// GoString keeps descriptor fingerprints stable when they contain native type
// pointers. It is used by the generator's %#v comparison tests.
func (n NativeTypeDef) GoString() string {
	var builder strings.Builder
	builder.WriteString("schema.NativeTypeDef{Dialect:")
	builder.WriteString(strconv.Quote(n.Dialect))
	builder.WriteString(", Name:")
	builder.WriteString(strconv.Quote(n.Name))
	builder.WriteString(", Kind:")
	builder.WriteString(strconv.Quote(string(n.Kind)))
	if n.Schema != "" {
		builder.WriteString(", Schema:")
		builder.WriteString(strconv.Quote(n.Schema))
	}
	if n.Arguments != nil {
		builder.WriteString(", Arguments:")
		fmt.Fprintf(&builder, "%#v", n.Arguments)
	}
	if n.Element != nil {
		builder.WriteString(", Element:&")
		builder.WriteString(n.Element.GoString())
	}
	builder.WriteByte('}')
	return builder.String()
}

func (OpaqueType) Kind() TypeKind { return KindNative }
func (OpaqueType) columnType()    {}

func (n *NativeTypeDef) clone() *NativeTypeDef {
	if n == nil {
		return nil
	}
	clone := *n
	if n.Arguments != nil {
		clone.Arguments = append([]string(nil), n.Arguments...)
	}
	clone.Element = n.Element.clone()
	return &clone
}

func (n *NativeTypeDef) validate(path string, depth int) error {
	if n == nil {
		return nil
	}
	if depth > 32 {
		return validationError(path, "native type nesting exceeds depth 32")
	}
	if n.Dialect == "" {
		return validationError(path+".dialect", "must not be empty")
	}
	if n.Name == "" {
		return validationError(path+".name", "must not be empty")
	}
	if n.Kind == "" {
		return validationError(path+".kind", "must not be empty")
	}
	switch n.Kind {
	case NativeBuiltin, NativeDomain, NativeEnum, NativeSet, NativeArray, NativeOther:
	default:
		return validationError(path+".kind", "is unsupported")
	}
	if n.Schema != "" {
		if err := ValidateIdentifier(n.Schema); err != nil {
			return validationError(path+".schema", "%s", err.Error())
		}
	}
	if n.Dialect == "postgresql" {
		if err := ValidateIdentifier(n.Name); err != nil {
			return validationError(path+".name", "%s", err.Error())
		}
	}
	if n.Dialect == "mysql" && (n.Kind == NativeDomain || n.Kind == NativeArray) {
		return validationError(path+".kind", "is unsupported for mysql")
	}
	if n.Dialect == "postgresql" && n.Kind == NativeSet {
		return validationError(path+".kind", "is unsupported for postgresql")
	}
	if n.Kind != NativeArray && n.Element != nil {
		return validationError(path+".element", "is only valid for array native types")
	}
	if n.Kind == NativeArray && n.Element == nil {
		return validationError(path+".element", "is required for array native types")
	}
	if err := n.Element.validate(path+".element", depth+1); err != nil {
		return err
	}
	return nil
}

func validateNativeColumn(column ColumnDef, path string) error {
	if err := column.NativeType.validate(path+".native_type", 1); err != nil {
		return err
	}
	if _, opaque := column.Type.(OpaqueType); opaque && column.NativeType == nil {
		return validationError(path+".native_type", "is required for opaque column types")
	}
	return nil
}
