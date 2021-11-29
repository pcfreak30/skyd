package renter

import (
	"encoding/json"
	"os"
	"path/filepath"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

const (
	// JSONLExtension is the file extension for a JSON log, which is a log file
	// that consists of JSON encoded data structures separated by newlines,
	// allowing the file to be read line by line.
	JSONLExtension = ".jsonl"
)

type (
	// JSONLogger is a logger that allows logging JSON objects to a file
	JSONLogger struct {
		staticFile *os.File
	}
)

// newJSONLogger creates a JSON logger at the given path
func newJSONLogger(path string) (*JSONLogger, error) {
	if filepath.Ext(path) != JSONLExtension {
		return nil, errors.New("invalid JSON log path, has to have the '.jsonl' extension")
	}

	// ensure the directory exists
	err := os.MkdirAll(filepath.Dir(path), skymodules.DefaultDirPerm)
	if err != nil {
		return nil, err
	}

	// open the file
	logFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, skymodules.DefaultFilePerm)
	if err != nil {
		return nil, err
	}
	return &JSONLogger{logFile}, nil
}

// WriteJSON will write the JSON encoded version of the given object to the
// underlying log file.
func (l *JSONLogger) WriteJSON(obj interface{}) error {
	jsonBytes, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	l.staticFile.Write(append(jsonBytes, []byte("\n")...))
	return nil
}

// Close calls close on the JSON logger's underlying log file.
func (l *JSONLogger) Close() error {
	return l.staticFile.Close()
}
