/*
FILE: logger.go

DESCRIPTION:
Public re-export of the SDK logging contract (implementation: internal/hllog).
The SDK depends on no logging library: the application adapts its own logger
(zerolog, zap, slog) to the 4-method Logger interface. Fields are typed values,
not ...any, so logging never boxes or allocates on hot paths.

The default logger is a no-op.
*/

package hyperliquid

import "github.com/tonymontanov/go-hyperliquid/internal/hllog"

// Logger — minimal SDK logging contract.
type Logger = hllog.Logger

// Field — typed key-value log field.
type Field = hllog.Field

// FieldKind — Field discriminator.
type FieldKind = hllog.FieldKind

// Field kinds.
const (
	FieldKindString FieldKind = hllog.FieldKindString
	FieldKindInt    FieldKind = hllog.FieldKindInt
	FieldKindFloat  FieldKind = hllog.FieldKindFloat
	FieldKindBool   FieldKind = hllog.FieldKindBool
	FieldKindError  FieldKind = hllog.FieldKindError
)

// Str creates a string Field.
func Str(key, value string) Field { return hllog.Str(key, value) }

// Int creates an integer Field.
func Int(key string, value int64) Field { return hllog.Int(key, value) }

// Float creates a float Field.
func Float(key string, value float64) Field { return hllog.Float(key, value) }

// Bool creates a boolean Field.
func Bool(key string, value bool) Field { return hllog.Bool(key, value) }

// Err creates an error Field.
func Err(err error) Field { return hllog.Err(err) }

// NoopLogger returns the no-op logger.
func NoopLogger() Logger { return hllog.Noop() }
