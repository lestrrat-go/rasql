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
	Scalars   []mappingScalar    `json:"scalars"`
	Relations *[]mappingRelation `json:"relations"`
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
type mappingRelation struct {
	Name    string         `json:"name"`
	Source  string         `json:"source"`
	From    []string       `json:"from"`
	Target  string         `json:"target"`
	To      []string       `json:"to"`
	Through mappingThrough `json:"through"`
}
type mappingThrough struct {
	Object     string   `json:"object"`
	SourceFrom []string `json:"source_from"`
	SourceTo   []string `json:"source_to"`
	TargetFrom []string `json:"target_from"`
	TargetTo   []string `json:"target_to"`
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
	if file.Relations != nil {
		config.Relations = make([]compilerir.RelationMapping, len(*file.Relations))
	}
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
	for i, relation := range slicesOrEmpty(file.Relations) {
		config.Relations[i] = compilerir.RelationMapping{
			Name: relation.Name, Source: compilerir.ObjectID(relation.Source), From: append([]string(nil), relation.From...),
			Target: compilerir.ObjectID(relation.Target), To: append([]string(nil), relation.To...),
			Through: compilerir.ThroughMapping{
				Object:     compilerir.ObjectID(relation.Through.Object),
				SourceFrom: append([]string(nil), relation.Through.SourceFrom...), SourceTo: append([]string(nil), relation.Through.SourceTo...),
				TargetFrom: append([]string(nil), relation.Through.TargetFrom...), TargetTo: append([]string(nil), relation.Through.TargetTo...),
			},
		}
	}
	if err := compilerir.ValidateMappingConfig(config, packageName); err != nil {
		return compilerir.MappingConfig{}, err
	}
	return config, nil
}

func slicesOrEmpty[T any](values *[]T) []T {
	if values == nil {
		return nil
	}
	return *values
}

// ValidateMappings validates an already decoded mapping policy.
func ValidateMappings(config compilerir.MappingConfig, packageName string) error {
	return compilerir.ValidateMappingConfig(config, packageName)
}
