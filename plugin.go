//go:build !excludevoice

// Package main satisfies the Nakama Go plugin interface.
// This file provides the build tag and ensures interface satisfaction.
package main

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Compile-time check that InitModule has the correct signature.
var _ func(
	ctx context.Context,
	logger runtime.Logger,
	db *sql.DB,
	nk runtime.NakamaModule,
	initializer runtime.Initializer,
) error = InitModule
