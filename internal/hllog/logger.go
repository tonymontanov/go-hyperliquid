/*
FILE: internal/hllog/logger.go

DESCRIPTION:
Logger interface + typed Field. Placed in internal so that any SDK package
(rest, ws, engine, sections) can use it without import cycles. The root
hyperliquid package re-exports the type/functions via aliases.

SECURITY NOTES:
The SDK never passes private keys, signatures or full request bodies of signed
actions to the logger. Addresses are public data and may be logged.
*/

package hllog

// FieldKind — Field discriminator.
type FieldKind uint8

const (
	// FieldKindString — Field.Str carries the value.
	FieldKindString FieldKind = iota
	// FieldKindInt — Field.Int carries the value.
	FieldKindInt
	// FieldKindFloat — Field.Flt carries the value.
	FieldKindFloat
	// FieldKindBool — Field.Bool carries the value.
	FieldKindBool
	// FieldKindError — Field.Err carries the value.
	FieldKindError
)

// Field — typed key-value log field. No interface — no boxing.
type Field struct {
	Key  string
	Kind FieldKind
	Str  string
	Int  int64
	Flt  float64
	Bool bool
	Err  error
}

// Str creates a string Field.
func Str(key, value string) Field {
	return Field{Key: key, Kind: FieldKindString, Str: value}
}

// Int creates an integer Field.
func Int(key string, value int64) Field {
	return Field{Key: key, Kind: FieldKindInt, Int: value}
}

// Float creates a float Field.
func Float(key string, value float64) Field {
	return Field{Key: key, Kind: FieldKindFloat, Flt: value}
}

// Bool creates a boolean Field.
func Bool(key string, value bool) Field {
	return Field{Key: key, Kind: FieldKindBool, Bool: value}
}

// Err creates an error Field under the conventional "error" key.
func Err(err error) Field {
	return Field{Key: "error", Kind: FieldKindError, Err: err}
}

// Logger — minimal SDK logging contract.
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
}

// noop — default implementation.
type noop struct{}

// Noop returns the singleton no-op logger.
func Noop() Logger { return noopSingleton }

var noopSingleton Logger = noop{}

func (noop) Debug(string, ...Field) {}
func (noop) Info(string, ...Field)  {}
func (noop) Warn(string, ...Field)  {}
func (noop) Error(string, ...Field) {}
