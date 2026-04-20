package main

// Embeds the openapi.yaml spec at compile time and serves it at
// /api/v1/openapi.json. We parse YAML → JSON on first request and cache
// the result so subsequent requests are a static byte slice.
//
// nanobot (and most LLM tool-calling clients) expect JSON for OpenAPI tool
// discovery; keeping the YAML as the authored source means humans can read
// and review the spec.

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed openapi.yaml
var openapiYAML []byte

var (
	openapiOnce sync.Once
	openapiJSON []byte
	openapiErr  error
)

func openapiSpecJSON() ([]byte, error) {
	openapiOnce.Do(func() {
		// yaml.v3 → any → json. We have to walk the tree to convert
		// map[any]any (a yaml.v3 quirk for non-string keys) into
		// map[string]any so encoding/json can serialize it. In practice
		// our spec only uses string keys, but be defensive.
		var n yaml.Node
		if err := yaml.Unmarshal(openapiYAML, &n); err != nil {
			openapiErr = err
			return
		}
		var v any
		if err := n.Decode(&v); err != nil {
			openapiErr = err
			return
		}
		v = normalizeForJSON(v)
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			openapiErr = err
			return
		}
		openapiJSON = b
	})
	return openapiJSON, openapiErr
}

func normalizeForJSON(v any) any {
	switch x := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, vv := range x {
			ks, ok := k.(string)
			if !ok {
				continue
			}
			m[ks] = normalizeForJSON(vv)
		}
		return m
	case map[string]any:
		for k, vv := range x {
			x[k] = normalizeForJSON(vv)
		}
		return x
	case []any:
		for i := range x {
			x[i] = normalizeForJSON(x[i])
		}
		return x
	default:
		return v
	}
}

func handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	b, err := openapiSpecJSON()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "openapi spec embed failed: " + err.Error(),
			"code":  "openapi_error",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}
