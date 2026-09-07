// Package compilerconfig decodes strict generation mapping policy.
package compilerconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

type mappingFile struct {
	Scalars []mappingScalar `json:"scalars"`
}
type mappingScalar struct {
	Name           string          `json:"name"`
	Match          mappingMatch    `json:"match"`
	GoType         string          `json:"go_type"`
	NullableGoType string          `json:"nullable_go_type"`
	Imports        []mappingImport `json:"imports"`
	Codec          string          `json:"codec"`
}
type mappingImport struct {
	Path  string `json:"path"`
	Alias string `json:"alias"`
}
type mappingMatch struct {
	Dialect     string `json:"dialect"`
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	LogicalKind string `json:"logical_kind"`
}

// DecodeMappings decodes a mapping object and rejects unknown or trailing JSON.
func DecodeMappings(data []byte, packageName string) (compilerir.MappingConfig, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file mappingFile
	if err := decoder.Decode(&file); err != nil {
		return compilerir.MappingConfig{}, fmt.Errorf("decode mappings: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return compilerir.MappingConfig{}, fmt.Errorf("decode mappings: trailing JSON")
		}
		return compilerir.MappingConfig{}, fmt.Errorf("decode mappings: %w", err)
	}
	config := compilerir.MappingConfig{Scalars: make([]compilerir.ScalarMapping, len(file.Scalars))}
	for i, scalar := range file.Scalars {
		config.Scalars[i] = compilerir.ScalarMapping{
			Name: scalar.Name, GoType: scalar.GoType, NullableGoType: scalar.NullableGoType,
			Imports: make([]compilerir.GoImport, len(scalar.Imports)), Codec: scalar.Codec,
			Match: compilerir.NativeMatch{Dialect: scalar.Match.Dialect, Schema: scalar.Match.Schema, Name: scalar.Match.Name, Kind: scalar.Match.Kind, LogicalKind: scalar.Match.LogicalKind},
		}
		for j, imp := range scalar.Imports {
			config.Scalars[i].Imports[j] = compilerir.GoImport{Path: imp.Path, Alias: imp.Alias}
		}
	}
	if err := compilerir.ValidateMappingConfig(config, packageName); err != nil {
		return compilerir.MappingConfig{}, err
	}
	return config, nil
}

// ValidateMappings validates an already decoded mapping policy.
func ValidateMappings(config compilerir.MappingConfig, packageName string) error {
	return compilerir.ValidateMappingConfig(config, packageName)
}
