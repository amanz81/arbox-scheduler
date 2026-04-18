package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/envfile"
)

// newAuthedClient returns an arboxapi.Client seeded with tokens from .env and
// with credentials set so it can silently re-login on 401. It also returns
// the parsed config (for timezone lookups).
//
// If no tokens are set yet, it performs an initial login using ARBOX_EMAIL /
// ARBOX_PASSWORD (which must be present).
func newAuthedClient(ctx context.Context) (*arboxapi.Client, *config.Config, error) {
	_ = envfile.Load(defaultEnvPath())

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("config invalid: %w", err)
	}

	email := os.Getenv("ARBOX_EMAIL")
	password := os.Getenv("ARBOX_PASSWORD")

	client := arboxapi.New(os.Getenv("ARBOX_BASE_URL"))
	client.Token = os.Getenv("ARBOX_TOKEN")
	client.RefreshToken = os.Getenv("ARBOX_REFRESH_TOKEN")
	if email != "" && password != "" {
		client.SetCredentials(email, password)
	}

	// If we have no token at all, log in synchronously so the first call
	// doesn't waste a roundtrip on a guaranteed 401.
	if client.Token == "" {
		if email == "" || password == "" {
			return nil, nil, errors.New("no ARBOX_TOKEN and no ARBOX_EMAIL/ARBOX_PASSWORD; run `arbox auth login`")
		}
		loginCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		r, err := client.LoginAndStore(loginCtx, email, password)
		if err != nil {
			return nil, nil, fmt.Errorf("initial login: %w", err)
		}
		// Persist the freshly fetched tokens.
		_ = envfile.Upsert(defaultEnvPath(), "ARBOX_TOKEN", r.Token)
		if r.RefreshToken != "" {
			_ = envfile.Upsert(defaultEnvPath(), "ARBOX_REFRESH_TOKEN", r.RefreshToken)
		}
	}
	return client, cfg, nil
}
