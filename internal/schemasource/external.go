package schemasource

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const maxCommandOutput = 1 << 20

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, r ProcessRequest) (ProcessResult, error) {
	if len(r.Argv) == 0 {
		return ProcessResult{}, fmt.Errorf("schema source: external command is empty")
	}
	cmd := exec.CommandContext(ctx, r.Argv[0], r.Argv[1:]...)
	cmd.Dir = r.Directory
	cmd.Env = append([]string(nil), r.Environment...)
	var stdout, stderr boundedWriter
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := ProcessResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		result.ExitCode = 1
		if exit, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exit.ExitCode()
			return result, nil
		}
		return result, err
	}
	return result, nil
}
func externalRequest(r Request, dsn string) ProcessRequest {
	values := make(map[string]string)
	for _, entry := range sanitizedEnvironment() {
		k, v, _ := strings.Cut(entry, "=")
		values[k] = v
	}
	for k, v := range r.Source.Environment {
		values[k] = v
	}
	values["RASQL_SCHEMA_DSN"] = dsn
	env := make([]string, 0, len(values))
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return ProcessRequest{Argv: append([]string(nil), r.Source.Command...), Environment: env, Directory: r.ModuleRoot}
}
func sanitizedEnvironment() []string {
	allowed := map[string]string{}
	for _, v := range os.Environ() {
		k, value, ok := strings.Cut(v, "=")
		if !ok {
			continue
		}
		if k == "PATH" || k == "HOME" || k == "USER" || k == "LANG" || strings.HasPrefix(k, "LC_") {
			allowed[k] = value
		}
	}
	out := make([]string, 0, len(allowed))
	for k, v := range allowed {
		out = append(out, k+"="+v)
	}
	return out
}
func bounded(b []byte) []byte {
	if len(b) > maxCommandOutput {
		return append([]byte(nil), b[:maxCommandOutput]...)
	}
	return append([]byte(nil), b...)
}
func redactMany(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "<redacted>")
		}
	}
	return s
}

type boundedWriter struct{ b bytes.Buffer }

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.b.Len() < maxCommandOutput {
		n := maxCommandOutput - w.b.Len()
		if len(p) < n {
			n = len(p)
		}
		_, _ = w.b.Write(p[:n])
	}
	return len(p), nil
}
func (w *boundedWriter) Bytes() []byte { return append([]byte(nil), w.b.Bytes()...) }
