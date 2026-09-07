package compilerquery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

type ValueDeclaration struct {
	Name     string
	Scalar   string
	Nullable *bool
}

type QueryConfig struct {
	ID          compilerir.QueryID
	Input       string
	Engine      string
	Function    string
	Output      string
	Operation   string
	Cardinality string
	Parameters  []ValueDeclaration
	Results     []ValueDeclaration
}

type Config struct {
	ModuleRoot string
	Mappings   compilerir.MappingConfig
	Queries    []QueryConfig
}

func DecodeConfig(data []byte) (Config, error) {
	var raw struct {
		Queries []json.RawMessage `json:"queries"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("compilerquery: decode config: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("compilerquery: trailing JSON data")
		}
		return Config{}, fmt.Errorf("compilerquery: trailing JSON data: %w", err)
	}
	queries := make([]QueryConfig, len(raw.Queries))
	for i, item := range raw.Queries {
		var q struct {
			ID          compilerir.QueryID `json:"id"`
			Input       string             `json:"input"`
			Engine      string             `json:"engine"`
			Function    string             `json:"function"`
			Output      string             `json:"output"`
			Operation   string             `json:"operation"`
			Cardinality string             `json:"cardinality"`
			Parameters  []ValueDeclaration `json:"parameters"`
			Results     []ValueDeclaration `json:"results"`
		}
		d := json.NewDecoder(bytes.NewReader(item))
		d.DisallowUnknownFields()
		if err := d.Decode(&q); err != nil {
			return Config{}, fmt.Errorf("compilerquery: query %d: %w", i, err)
		}
		queries[i] = QueryConfig{ID: q.ID, Input: q.Input, Engine: q.Engine, Function: q.Function, Output: q.Output, Operation: q.Operation, Cardinality: q.Cardinality, Parameters: q.Parameters, Results: q.Results}
	}
	return Config{Queries: queries}, nil
}

func ValidateConfig(c Config, engine compilerir.EngineIdentity) error {
	if strings.TrimSpace(c.ModuleRoot) == "" {
		return fmt.Errorf("compilerquery: module root is required")
	}
	if filepath.IsAbs(c.ModuleRoot) && filepath.Clean(c.ModuleRoot) != c.ModuleRoot {
		return fmt.Errorf("compilerquery: module root must be clean")
	}
	seenID := map[compilerir.QueryID]struct{}{}
	seenInput := map[string]struct{}{}
	seenFunction := map[string]struct{}{}
	seenOutput := map[string]struct{}{}
	for i, q := range c.Queries {
		if q.ID == "" || q.Input == "" || q.Function == "" {
			return fmt.Errorf("compilerquery: query %d requires id, input, and function", i)
		}
		if filepath.IsAbs(q.Input) || filepath.Clean(q.Input) != q.Input || strings.Contains(q.Input, ".."+string(filepath.Separator)) {
			return fmt.Errorf("compilerquery: query %q input must be module-relative", q.ID)
		}
		if q.Output != "" {
			if filepath.IsAbs(q.Output) || filepath.Clean(q.Output) != q.Output || strings.Contains(q.Output, ".."+string(filepath.Separator)) {
				return fmt.Errorf("compilerquery: query %q output must be module-relative", q.ID)
			}
			if _, ok := seenOutput[q.Output]; ok {
				return fmt.Errorf("compilerquery: duplicate query output %q", q.Output)
			}
			seenOutput[q.Output] = struct{}{}
		}
		if _, ok := seenID[q.ID]; ok {
			return fmt.Errorf("compilerquery: duplicate query id %q", q.ID)
		}
		seenID[q.ID] = struct{}{}
		if _, ok := seenInput[q.Input]; ok {
			return fmt.Errorf("compilerquery: duplicate query input %q", q.Input)
		}
		seenInput[q.Input] = struct{}{}
		if _, ok := seenFunction[q.Function]; ok {
			return fmt.Errorf("compilerquery: duplicate query function %q", q.Function)
		}
		seenFunction[q.Function] = struct{}{}
		if q.Engine == "" {
			return fmt.Errorf("compilerquery: query %q engine is required", q.ID)
		}
		if !strings.EqualFold(q.Engine, engine.Dialect) {
			return fmt.Errorf("compilerquery: query %q engine %q differs from %q", q.ID, q.Engine, engine.Dialect)
		}
		if q.Operation == "" {
			return fmt.Errorf("compilerquery: query %q operation is required", q.ID)
		}
		if q.Cardinality == "" {
			return fmt.Errorf("compilerquery: query %q cardinality is required", q.ID)
		}
		for _, values := range [][]ValueDeclaration{q.Parameters, q.Results} {
			seen := map[string]struct{}{}
			for _, value := range values {
				if value.Name == "" {
					return fmt.Errorf("compilerquery: query %q has blank value name", q.ID)
				}
				if _, ok := seen[value.Name]; ok {
					return fmt.Errorf("compilerquery: query %q has duplicate value %q", q.ID, value.Name)
				}
				seen[value.Name] = struct{}{}
				if value.Nullable == nil {
					return fmt.Errorf("compilerquery: query %q value %q must declare nullable", q.ID, value.Name)
				}
			}
		}
	}
	return nil
}
