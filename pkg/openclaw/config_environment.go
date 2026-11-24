package openclaw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// profileEnvironment freezes ordinary authored variables while removing route
// selectors. The native config CLI redacts every env.vars value. In the common
// standalone JSON file, read the exact authored values without a second launch.
func (g *gateway) profileEnvironment(ctx context.Context, path string) (map[string]json.RawMessage, error) {
	if source, ok := directProfileEnvironment(path); ok {
		return withoutRouteEnvironment(source)
	}
	data, err := g.profileSection(ctx, "env")
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var source map[string]json.RawMessage
	if json.Unmarshal(data, &source) != nil {
		return nil, errors.New("cannot read the OpenClaw profile environment")
	}
	clean, err := withoutRouteEnvironment(source)
	if err != nil {
		return nil, err
	}
	for key := range clean {
		// The native read can accurately preserve shellEnv settings. For
		// credential variables, do not substitute the CLI's redacted values.
		if key != "shellEnv" {
			return nil, errors.New("OpenClaw cannot pin authored environment variables from this config; use the native profile .env or exported variables")
		}
	}
	return clean, nil
}

func directProfileEnvironment(path string) (map[string]json.RawMessage, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	const maxBytes = 8 << 20
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return nil, false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root["$include"] != nil {
		return nil, false
	}
	if root["env"] == nil {
		return nil, true
	}
	var env map[string]json.RawMessage
	if json.Unmarshal(root["env"], &env) != nil || env["$include"] != nil {
		return nil, false
	}
	var vars map[string]json.RawMessage
	if env["vars"] != nil && (json.Unmarshal(env["vars"], &vars) != nil || vars["$include"] != nil) {
		return nil, false
	}
	// A separate include can also hide allowed shellEnv settings. Ask the
	// native loader to resolve those when the branch uses such a directive.
	var shell map[string]json.RawMessage
	if env["shellEnv"] != nil && (json.Unmarshal(env["shellEnv"], &shell) != nil || shell["$include"] != nil) {
		return nil, false
	}
	return env, true
}

func withoutRouteEnvironment(env map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	for key := range env {
		if isRouteEnvironment(key) {
			delete(env, key)
		}
	}
	if data := env["vars"]; data != nil {
		var vars map[string]string
		if json.Unmarshal(data, &vars) != nil {
			return nil, errors.New("cannot pin the OpenClaw profile environment")
		}
		for key := range vars {
			if isRouteEnvironment(key) {
				delete(vars, key)
			}
		}
		if len(vars) == 0 {
			delete(env, "vars")
		} else {
			env["vars"], _ = json.Marshal(vars)
		}
	}
	return env, nil
}

func isRouteEnvironment(key string) bool {
	switch strings.ToUpper(strings.TrimSpace(key)) {
	case gatewayURLVariable, gatewayPortVariable:
		return true
	default:
		return false
	}
}
