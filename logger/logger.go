// Copyright (C) 2014 Jakob Borg and Contributors (see the CONTRIBUTORS file).
// All rights reserved. Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package logger implements a standardized logger with callback functionality
package logger

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	// begin of the recoded code
	"filemanager"
	"io"
	"time"
	// end of the recoded code
)

type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelOK
	LevelWarn
	LevelFatal
	NumLevels
)

type MessageHandler func(l LogLevel, msg string)

type Logger struct {
	logger   *log.Logger
	handlers [NumLevels][]MessageHandler
	mut      sync.Mutex
}

var DefaultLogger = New()

func New() *Logger {
	return &Logger{
		logger: log.New(os.Stdout, "[start] ", log.Ltime),
	}
}

// begin of the recoded code

var RecodedLogger *Logger
var LogPrefix string

func (l *Logger) Get() *log.Logger {
	return l.logger
}

func (l *Logger) Set(val *log.Logger) {
	l.logger = val
}

func CreateFileConsoleLogger(dir string) *Logger {
	dir += string(os.PathSeparator) + "service" + string(os.PathSeparator)
	filemanager.CreateDir(dir)
	n := time.Now().UTC()
	logFileName := fmt.Sprintf("%4d-%02d-%02d_%02d-%02d.log",
		n.Year(), int(n.Month()), n.Day(), n.Hour(), n.Minute())
	output := dir + logFileName
	file, err := os.OpenFile(output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Printf("Failed to open log file: %s, Error: %s\n", output, err)
	}
	multi := io.MultiWriter(file, os.Stdout)
	return &Logger{
		logger: log.New(multi, "[start] ", log.Ldate|log.Ltime),
	}
}

func (l *Logger) SetPrefix(prefix string) {
	l.logger.SetPrefix(prefix)
}

func (l *Logger) GetPrefix() string {
	return l.logger.Prefix()
}

// end of the recoded code

func (l *Logger) AddHandler(level LogLevel, h MessageHandler) {
	l.mut.Lock()
	defer l.mut.Unlock()
	l.handlers[level] = append(l.handlers[level], h)
}

func (l *Logger) SetFlags(flag int) {
	l.logger.SetFlags(flag)
}

func (l *Logger) callHandlers(level LogLevel, s string) {
	for _, h := range l.handlers[level] {
		h(level, strings.TrimSpace(s))
	}
}

func (l *Logger) Debugln(prefix string, vals ...interface{}) {
	l.mut.Lock()
	defer l.mut.Unlock()
	p := l.GetPrefix()
	l.SetPrefix(prefix)
	s := fmt.Sprintln(vals...)
	l.logger.Output(2, "DEBUG: "+s)
	l.callHandlers(LevelDebug, s)
	l.SetPrefix(p)
}

func (l *Logger) Debugf(prefix string, format string, vals ...interface{}) {
	l.mut.Lock()
	defer l.mut.Unlock()
	p := l.GetPrefix()
	l.SetPrefix(prefix)
	s := fmt.Sprintf(format, vals...)
	l.logger.Output(2, "DEBUG: "+s)
	l.callHandlers(LevelDebug, s)
	l.SetPrefix(p)
}
func (l *Logger) Infoln(prefix string, vals ...interface{}) {
	l.mut.Lock()
	defer l.mut.Unlock()
	p := l.GetPrefix()
	l.SetPrefix(prefix)
	s := fmt.Sprintln(vals...)
	l.logger.Output(2, "INFO: "+s)
	l.callHandlers(LevelInfo, s)
	l.SetPrefix(p)
}

func (l *Logger) Infof(prefix string, format string, vals ...interface{}) {
	l.mut.Lock()
	defer l.mut.Unlock()
	p := l.GetPrefix()
	l.SetPrefix(prefix)
	s := fmt.Sprintf(format, vals...)
	l.logger.Output(2, "INFO: "+s)
	l.callHandlers(LevelInfo, s)
	l.SetPrefix(p)
}

func (l *Logger) Okln(prefix string, vals ...interface{}) {
	l.mut.Lock()
	defer l.mut.Unlock()
	p := l.GetPrefix()
	l.SetPrefix(prefix)
	s := fmt.Sprintln(vals...)
	l.logger.Output(2, "OK: "+s)
	l.callHandlers(LevelOK, s)
	l.SetPrefix(p)
}

func (l *Logger) Okf(prefix string, format string, vals ...interface{}) {
	l.mut.Lock()
	defer l.mut.Unlock()
	p := l.GetPrefix()
	l.SetPrefix(prefix)
	s := fmt.Sprintf(format, vals...)
	l.logger.Output(2, "OK: "+s)
	l.callHandlers(LevelOK, s)
	l.SetPrefix(p)
}

func (l *Logger) Warnln(prefix string, vals ...interface{}) {
	l.mut.Lock()
	defer l.mut.Unlock()
	p := l.GetPrefix()
	l.SetPrefix(prefix)
	s := fmt.Sprintln(vals...)
	l.logger.Output(2, "WARNING: "+s)
	l.callHandlers(LevelWarn, s)
	l.SetPrefix(p)
}

func (l *Logger) Warnf(prefix string, format string, vals ...interface{}) {
	l.mut.Lock()
	defer l.mut.Unlock()
	p := l.GetPrefix()
	l.SetPrefix(prefix)
	s := fmt.Sprintf(format, vals...)
	l.logger.Output(2, "WARNING: "+s)
	l.callHandlers(LevelWarn, s)
	l.SetPrefix(p)
}

func (l *Logger) Fatalln(prefix string, vals ...interface{}) {
	l.mut.Lock()
	defer l.mut.Unlock()
	l.SetPrefix(prefix)
	s := fmt.Sprintln(vals...)
	l.logger.Output(2, "FATAL: "+s)
	l.callHandlers(LevelFatal, s)
	os.Exit(1)
}

func (l *Logger) Fatalf(prefix string, format string, vals ...interface{}) {
	l.mut.Lock()
	defer l.mut.Unlock()
	l.SetPrefix(prefix)
	s := fmt.Sprintf(format, vals...)
	l.logger.Output(2, "FATAL: "+s)
	l.callHandlers(LevelFatal, s)
	os.Exit(1)
}

func (l *Logger) FatalErr(prefix string, err error) {
	if err != nil {
		l.mut.Lock()
		defer l.mut.Unlock()
		l.SetPrefix(prefix)
		l.logger.SetFlags(l.logger.Flags() | log.Lshortfile)
		l.logger.Output(2, "FATAL: "+err.Error())
		l.callHandlers(LevelFatal, err.Error())
		os.Exit(1)
	}
}
