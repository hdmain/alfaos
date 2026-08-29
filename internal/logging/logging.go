package logging

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelSuccess
)

var (
	mu     sync.Mutex
	level  = LevelInfo
	output io.Writer = os.Stdout
)

func SetLevel(l Level) { level = l }
func SetOutput(w io.Writer) { output = w }

func Debug(format string, args ...any) { log(LevelDebug, "DEBUG", format, args...) }
func Info(format string, args ...any)  { log(LevelInfo, "INFO", format, args...) }
func Warn(format string, args ...any)  { log(LevelWarn, "WARN", format, args...) }
func Error(format string, args ...any) { log(LevelError, "ERROR", format, args...) }
func Success(format string, args ...any) { log(LevelSuccess, "OK", format, args...) }

func Step(n, total int, msg string) {
	Info("[%d/%d] %s", n, total, msg)
}

func Check(name string, ok bool, detail string) {
	mu.Lock()
	defer mu.Unlock()
	if ok {
		fmt.Fprintf(output, "  %-32s ✓", name)
		if detail != "" {
			fmt.Fprintf(output, "  (%s)", detail)
		}
		fmt.Fprintln(output)
	} else {
		fmt.Fprintf(output, "  %-32s ✗", name)
		if detail != "" {
			fmt.Fprintf(output, "  (%s)", detail)
		}
		fmt.Fprintln(output)
	}
}

func log(l Level, tag, format string, args ...any) {
	if l < level && l != LevelError {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	ts := time.Now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(output, "[%s] %s: %s\n", ts, tag, msg)
}

func Banner() {
	fmt.Fprint(output, `
    ___    __    ________  _____
   /   |  / /   / ____/  |/  / /
  / /| | / /   / /   / /|_/ / /
 / ___ |/ /___/ /___/ /  / / /___
/_/  |_/_____/\____/_/  /_/_____/

`)
	fmt.Fprintln(output, "  Automated Linux Framework for Alpha OS")
	fmt.Fprintln(output, "  ─────────────────────────────────────")
	fmt.Fprintln(output)
}
