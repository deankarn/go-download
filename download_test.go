package download

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// NOTES:
// - Run "go test" to run tests
// - Run "gocov test | gocov report" to report on test converage by file
// - Run "gocov test | gocov annotate -" to report on all code and functions, those ,marked with "MISS" were never called
//
// or
//
// -- may be a good idea to change to output path to somewherelike /tmp
// go test -coverprofile cover.out && go tool cover -html=cover.out -o cover.html

const (
	data           = "./testdata/data.txt"
	filesize int64 = 1e8
)

func TestMain(m *testing.M) {

	// create test data
	f, err := os.Create(data)
	if err != nil {
		log.Fatal(err)
	}

	if err := f.Truncate(filesize); err != nil {
		log.Fatal(err)
	}

	if err = f.Close(); err != nil {
		log.Fatal(err)
	}

	exitCode := m.Run()

	os.Remove(data)

	os.Exit(exitCode)
}

func CountBytes(r io.Reader) (count int64) {

	buf := make([]byte, bytes.MinRead)

	for {
		n, err := r.Read(buf)
		count += int64(n)

		if err != nil {
			return
		}
	}
}

func TestDownloadBasic(t *testing.T) {

	fs := http.StripPrefix("/testdata/", http.FileServer(http.Dir("./testdata")))

	mux := http.NewServeMux()
	mux.Handle("/testdata/", fs)

	server := httptest.NewServer(mux)
	defer server.Close()

	url := server.URL + "/testdata/data.txt"

	f, err := Open(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}

	num := CountBytes(f)
	if num != filesize {
		t.Fatalf("Invalid file size, expected '%d' got '%d'", filesize, num)
	}

	if fi.Size() != filesize {
		t.Fatalf("Invalid Content Length, expected '%d' got '%d'", filesize, fi.Size())
	}

	// check for stat error
	f.Close()

	fi, err = f.Stat()
	if fi != nil {
		t.Fatal("FileInfo should be nil")
	}

	if _, ok := err.(*os.PathError); !ok {
		log.Fatal("Expected os.PathError type")
	}
}
