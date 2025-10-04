package utils

import (
	"log"
	"os"
	"sync"
)

type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARNING
	ERROR
)

type Logger struct {
	mu       sync.Mutex
	logLevel LogLevel
	logger   *log.Logger
}

var loggerInstance *Logger
var once sync.Once

func GetLogger() *Logger {
	once.Do(func() {
		loggerInstance = &Logger{
			logLevel: INFO,
			logger:   log.New(os.Stdout, "", log.LstdFlags),
		}
	})
	return loggerInstance
}

func (l *Logger) log(level LogLevel, message string) {
	if level >= l.logLevel {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.logger.Println(message)
	}
}

func (l *Logger) Debug(message string) {
	l.log(DEBUG, "DEBUG: "+message)
}

func (l *Logger) Info(message string) {
	l.log(INFO, "INFO: "+message)
}

func (l *Logger) Warning(message string) {
	l.log(WARNING, "WARNING: "+message)
}

func (l *Logger) Error(message string) {
	l.log(ERROR, "ERROR: "+message)
}

func (l *Logger) SetLogLevel(level LogLevel) {
	l.logLevel = level
}
