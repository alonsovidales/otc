// SPDX-License-Identifier: AGPL-3.0-or-later

package log

import (
	"fmt"
	logger "log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DEBUG used to specify the "debug" level in order to print all the log messages
	DEBUG = 1
	// INFO used to specify the "info" level in order to print the info + error + fatal messages
	INFO = 2
	// ERROR used to specify the "error" level in order to print the error + fatal messages
	ERROR = 3
	// FATAL used to specify the "fatal" level in order to print only the fatal log lines
	FATAL = 4
)

var level = 0
var file *os.File
var path string
var maxSize int64
var mutex = new(sync.Mutex)

// Levels Different allowed debugging levels, the allowed levels are: DEBUG,
// INFO, ERROR, FATAL
var Levels = map[string]int{
	"DEBUG": DEBUG,
	"INFO":  INFO,
	"ERROR": ERROR,
	"FATAL": FATAL,
}

// ParseLevel reads a configured level whatever its case ("info", "Info",
// "INFO"). Issue #162: Levels' keys are upper-case while every config
// says level=info, so the lookup missed, gave 0 - DEBUG - and everything
// was logged, request paths and addresses included. An unknown name is an
// error, not a silent DEBUG.
func ParseLevel(name string) (int, error) {
	l, ok := Levels[strings.ToUpper(strings.TrimSpace(name))]
	if !ok {
		return 0, fmt.Errorf("unknown log level %q (use debug, info, error or fatal)", name)
	}
	return l, nil
}

// cKeepRotated is how many rotated logs are kept next to the live one;
// older ones are deleted (issue #162: they were kept forever).
const cKeepRotated = 5

// SetLogger Sets the global logger level, and the path and size of the log
// file to be used as output for the logs, in case of this method is not
// called, all the logs will be print on the standar output
func SetLogger(newLevel int, filePath string, maxSizeMB int64) {
	level = newLevel
	maxSize = maxSizeMB * 1024000
	mutex.Lock()
	setLogFile(filePath)
	mutex.Unlock()
}

// Debug Adds a new log line to the logs file in case of being in a DEBUG level
// or higer
func Debug(v ...interface{}) {
	if level <= DEBUG {
		_, file, line, _ := runtime.Caller(1)
		fileParts := strings.Split(file, "/")
		newLog(fmt.Sprintf("DEBUG: <%s:%d> ", fileParts[len(fileParts)-1], line), v...)
	}
}

// Info Adds a new log line to the logs file in case of being in a INFO level
// or higer
func Info(v ...interface{}) {
	if level <= INFO {
		newLog("INFO: ", v...)
	}
}

// Error Adds a new log line to the logs file in case of being in a ERROR level
// or higer
func Error(v ...interface{}) {
	errorCount.Add(1)
	if level <= ERROR {
		_, file, line, _ := runtime.Caller(1)
		fileParts := strings.Split(file, "/")
		newLog(fmt.Sprintf("ERROR: <%s:%d> ", fileParts[len(fileParts)-1], line), v...)
	}
}

// errorCount is how many times Error was called since the start.
var errorCount atomic.Uint64

// ErrorCount is how many errors were logged since the process started (the
// bridge's Fleet report counts the recent ones).
func ErrorCount() uint64 { return errorCount.Load() }

// Fatal Adds a new log line to the logs file and interrupts the execution of
// the application
func Fatal(v ...interface{}) {
	if level <= FATAL {
		_, file, line, _ := runtime.Caller(1)
		fileParts := strings.Split(file, "/")
		newLog(fmt.Sprintf("FATAL: <%s:%d> ", fileParts[len(fileParts)-1], line), v...)
	}
	os.Exit(1)
}

// die is Fatal for code that already holds mutex: Fatal would take it
// again and hang the process (and every goroutine that logs) instead of
// exiting. The log file can't be trusted any more, so the line goes to
// stderr (journald).
func die(v ...interface{}) {
	_, f, line, _ := runtime.Caller(1)
	logger.SetOutput(os.Stderr)
	logger.Print(fmt.Sprintf("FATAL: <%s:%d> ", filepath.Base(f), line), strings.TrimRight(fmt.Sprintln(v...), "\n"), "\n")
	os.Exit(1)
}

// setLogFile Sets the specified path as new log file, in case of have defined
// a previous log file, rotates this. Called with mutex held.
func setLogFile(filePath string) {
	if file != nil {
		file.Close()
		file = nil
		os.Rename(path, fmt.Sprintf("%s_%d.old", path, int32(time.Now().Unix())))
		pruneRotated(path)
	}

	path = filePath
	if outFile, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		// Private: 0o600 above only applies when the file is created, and
		// the device's used to be readable by every account on it.
		outFile.Chmod(0o600)
		file = outFile
		logger.SetOutput(file)
	} else {
		die("Can't open the log file:", filePath)
	}
}

// pruneRotated keeps the newest cKeepRotated "<path>_<unix>.old" files.
func pruneRotated(path string) {
	old, _ := filepath.Glob(path + "_*.old")
	if len(old) <= cKeepRotated {
		return
	}
	// The names end in the rotation time; same width until 2286, so a
	// lexical sort is a chronological one.
	sort.Strings(old)
	for _, f := range old[:len(old)-cKeepRotated] {
		os.Remove(f)
	}
}

// newLog Adds a new log line to the logger file with the specified level at
// the begging
func newLog(l string, v ...interface{}) {
	mutex.Lock()
	if file != nil {
		fStat, err := file.Stat()
		if err != nil {
			die("Can't stat logger file")
		}
		if fStat.Size() > maxSize {
			fmt.Println("ROTATE", fStat.Size(), maxSize)
			logger.Print("Rotating log file")
			setLogFile(path)
		}
	}
	mutex.Unlock()
	// One entry, one line (issue #162): a value from a request - a domain,
	// an owner id - carrying a newline could otherwise write a line that
	// looks like the device's own.
	msg := strings.TrimRight(fmt.Sprintln(v...), "\n")
	msg = strings.NewReplacer("\n", "\\n", "\r", "\\r").Replace(msg)
	logger.Print(l, msg, "\n")
}
