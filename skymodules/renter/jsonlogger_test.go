package renter

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"gitlab.com/SkynetLabs/skyd/build"
)

// testJSONStruct is a helper struct
type testJSONStruct struct {
	Foo string
	Bar string
}

// TestJSONLogger is a small unit test that verifies the functionality of the
// JSON logger type.
func TestJSONLogger(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// verify we assert the correct file extension
	testdir := build.TempDir(t.Name())
	testpath := filepath.Join(testdir, "file.txt")
	_, err := newJSONLogger(testpath)
	if err == nil {
		t.Fatal("expected invalid extension error")
	}

	// verify we can create a logger
	testpath = filepath.Join(testdir, "file.jsonl")
	logger, err := newJSONLogger(testpath)
	if err != nil {
		t.Fatal("unexpected error", err)
	}

	// verify we can't write json for types that do not supported marshaling
	err = logger.WriteJSON(make(chan int))
	if err == nil {
		t.Fatal("expected unsupported type error")
	}
	_, ok := err.(*json.UnsupportedTypeError)
	if !ok {
		t.Fatal("expected unsupported type error, instead we received", err)
	}
	err = logger.WriteJSON(math.Inf(1))
	if err == nil {
		t.Fatal("expected unsupported value error")
	}
	_, ok = err.(*json.UnsupportedValueError)
	if !ok {
		t.Fatal("expected unsupported value error, instead we received", err)
	}

	// verify we can write to the file
	random := testJSONStruct{"baz", "bazzz"}
	err = logger.WriteJSON(random)
	if err != nil {
		t.Fatal("unexpected error", err)
	}

	// write one more line to prove we can append
	random = testJSONStruct{"hello", "world"}
	err = logger.WriteJSON(random)
	if err != nil {
		t.Fatal("unexpected error", err)
	}

	// close the logger
	err = logger.Close()
	if err != nil {
		t.Fatal("unexpected error", err)
	}

	// open the file manually
	file, err := os.Open(testpath)
	if err != nil {
		t.Fatal("unexpected error", err)
	}
	defer func() {
		err := file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// create a scanner to read the file line per line
	lines := make([]testJSONStruct, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		// extract the json
		line := scanner.Text()

		// unmarshal it
		var obj testJSONStruct
		err = json.Unmarshal([]byte(line), &obj)
		if err != nil {
			t.Fatal("unexpected error", err)
		}
		lines = append(lines, obj)
	}

	// check whether we encountered an error scanning
	err = scanner.Err()
	if err != nil {
		t.Fatal("unexpected error", err)
	}

	// check whether we found two log entries
	if len(lines) != 2 {
		t.Fatalf("unexpected amount of lines, %v != %v", len(lines), 2)
	}

	// check whether the data is correct
	obj1 := lines[0]
	obj2 := lines[1]
	if obj1.Foo != "baz" || obj1.Bar != "bazzz" {
		t.Fatal("unexpected value", obj1)
	}
	if obj2.Foo != "hello" || obj2.Bar != "world" {
		t.Fatal("unexpected value", obj2)
	}
}
