// Package log is deliberate noise: the logger-method heuristic should drop
// every call to it.
package log

type Logger struct{}

func New() *Logger { return &Logger{} }

func (l *Logger) Debug(msg string, args ...any) {}
func (l *Logger) Info(msg string, args ...any)  {}
func (l *Logger) Warn(msg string, args ...any)  {}
func (l *Logger) Error(msg string, args ...any) {}
