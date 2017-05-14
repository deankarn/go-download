package download

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestBadOptions(t *testing.T) {

	fs := http.StripPrefix("/testdata/", http.FileServer(http.Dir("./testdata")))

	mux := http.NewServeMux()
	mux.Handle("/testdata/", fs)
	mux.HandleFunc("/testdata/bad-head-response-code", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/testdata/good-head-bad-get", func(w http.ResponseWriter, r *http.Request) {

		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}

		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/testdata/bad-content-length", func(w http.ResponseWriter, r *http.Request) {

		fi, _ := os.Stat(data)

		w.Header().Add("Accept-Ranges", "bytes")
		w.Header().Add("Last-Modified", fi.ModTime().UTC().String())
		w.Header().Add("Content-Type", "text/plain; charset=utf-8")

		if r.Method == http.MethodHead {
			return
		}

		f, _ := os.Open(data)
		defer f.Close()

		io.Copy(w, f)
	})

	mux.HandleFunc("/testdata/good-head-bad-partial", func(w http.ResponseWriter, r *http.Request) {

		fi, _ := os.Stat(data)

		w.Header().Add("Accept-Ranges", "bytes")
		w.Header().Add("Content-Length", strconv.FormatInt(fi.Size(), 10))
		w.Header().Add("Last-Modified", fi.ModTime().UTC().String())
		w.Header().Add("Content-Type", "text/plain; charset=utf-8")

		if r.Method == http.MethodHead {
			return
		}

		w.WriteHeader(http.StatusNotFound)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	url := server.URL + "/testdata/bad-head-response-code"

	_, err := Open(url, nil)
	if _, ok := err.(*InvalidResponseCode); !ok {
		t.Fatal("Expected error to be of type *InvalidResponseCode")
	}

	expected := "Invalid response code, received '404' expected '200'"

	if err.Error() != expected {
		t.Fatalf("Expected '%s' got '%s'", expected, err.Error())
	}

	url = server.URL + "/testdata/good-head-bad-get"

	_, err = Open(url, nil)
	if _, ok := err.(*InvalidResponseCode); !ok {
		t.Fatal("Expected error to be of type *InvalidResponseCode")
	}

	expected = "Invalid response code, received '404' expected '200'"

	if err.Error() != expected {
		t.Fatalf("Expected '%s' got '%s'", expected, err.Error())
	}

	_, err = Open("", nil)
	if err == nil {
		t.Fatal("Expected error. got <nil>")
	}

	url = server.URL + "/testdata/bad-content-length"

	expected = "Invalid content length '-1'"

	_, err = Open(url, nil)
	if err == nil || err.Error() != expected {
		t.Fatalf("Expected '%s' got '%s'", expected, err)
	}

	url = server.URL + "/testdata/good-head-bad-partial"

	expected = "Invalid response code, received '404' expected '206'"

	_, err = Open(url, nil)
	if err == nil || err.Error() != expected {
		t.Fatalf("Expected '%s' got '%s'", expected, err)
	}

	url = server.URL + "/testdata/data.txt"
	options := &Options{
		Concurrency: func(size int64) int {
			return 0
		},
	}

	f, err := Open(url, options)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
}

func TestDownloadRangeBasic(t *testing.T) {

	fs := http.StripPrefix("/testdata/", http.FileServer(http.Dir("./testdata")))

	mux := http.NewServeMux()
	mux.Handle("/testdata/", fs)

	server := httptest.NewServer(mux)
	defer server.Close()

	url := server.URL + "/testdata/data.txt"

	options := &Options{
		Proxy: func(name string, download int, size int64, r io.Reader) io.Reader {
			return r // just mocking consumption
		},
	}

	f, err := Open(url, options)
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

	filename := "data.txt"

	if fi.Name() != filename {
		t.Fatalf("Wrong filename, expected '%s' got '%s'", filename, fi.Name())
	}

	if fi.Mode() != fileMode {
		t.Fatalf("Wrong filename, expected '%s' got '%s'", fileMode, fi.Mode())
	}

	if fi.IsDir() != false {
		t.Fatalf("Wrong filename, expected '%t' got '%t'", false, fi.IsDir())
	}

	if fi.Sys() != nil {
		t.Fatalf("Wrong filename, expected '%v' got '%v'", nil, fi.Sys())
	}

	if fi.ModTime().IsZero() {
		t.Fatal("ModTime empty")
	}

	// check for stat error
	f.Close()

	fi, err = f.Stat()
	if fi != nil {
		t.Fatal("FileInfo should be nil")
	}

	if _, ok := err.(*os.PathError); !ok {
		t.Fatal("Expected os.PathError type")
	}
}

func TestDownloadBasic(t *testing.T) {

	mux := http.NewServeMux()
	mux.HandleFunc("/testdata/", func(w http.ResponseWriter, r *http.Request) {

		fi, _ := os.Stat(data)

		w.Header().Add("Content-Length", strconv.FormatInt(fi.Size(), 10))
		w.Header().Add("Last-Modified", fi.ModTime().UTC().String())
		w.Header().Add("Content-Type", "text/plain; charset=utf-8")

		if r.Method == http.MethodHead {
			return
		}

		f, _ := os.Open(data)
		defer f.Close()

		io.Copy(w, f)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	url := server.URL + "/testdata/data.txt"

	options := &Options{
		Proxy: func(name string, download int, size int64, r io.Reader) io.Reader {
			return r // just mocking consumption
		},
	}

	f, err := Open(url, options)
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
		t.Fatal("Expected os.PathError type")
	}
}

func TestContextCancel(t *testing.T) {

	var m sync.Mutex
	increment := 1

	fs := http.StripPrefix("/testdata/", http.FileServer(http.Dir("./testdata")))

	mux := http.NewServeMux()
	mux.HandleFunc("/testdata/", func(w http.ResponseWriter, r *http.Request) {

		m.Lock()
		increment++
		duration := time.Duration(int(time.Millisecond) * increment)
		m.Unlock()

		time.Sleep(duration)

		fs.ServeHTTP(w, r)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	url := server.URL + "/testdata/data.txt"

	options := &Options{
		Concurrency: func(size int64) int {
			return 30
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	// cancel prematurely
	go func() {
		time.Sleep(time.Millisecond * 15)
		cancel()
	}()

	_, err := OpenContext(ctx, url, options)
	if err == nil {
		t.Fatal(err)
	}

	if _, ok := err.(*Canceled); !ok {
		t.Fatal("Expected error to be of type *Canceled")
	}

	prefix := "Download canceled for"
	suffix := "/testdata/data.txt'"

	if !strings.HasPrefix(err.Error(), prefix) || !strings.HasSuffix(err.Error(), suffix) {
		t.Fatalf("Expected prefix '%s' and suffix '%s' but got '%s'", prefix, suffix, err.Error())
	}

	// now that we've cancelled...lets see if we can't resume the download

	f, err := Open(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	num := CountBytes(f)
	if num != filesize {
		t.Fatalf("Invalid file size, expected '%d' got '%d'", filesize, num)
	}
}

func TestContextTimeout(t *testing.T) {

	var m sync.Mutex
	increment := 1

	fs := http.StripPrefix("/testdata/", http.FileServer(http.Dir("./testdata")))

	mux := http.NewServeMux()
	mux.HandleFunc("/testdata/", func(w http.ResponseWriter, r *http.Request) {

		m.Lock()
		increment++
		duration := time.Duration(int(time.Millisecond) * increment)
		m.Unlock()

		time.Sleep(duration)

		fs.ServeHTTP(w, r)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	url := server.URL + "/testdata/data.txt"

	options := &Options{
		Concurrency: func(size int64) int {
			return 20
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*25)
	defer cancel()

	_, err := OpenContext(ctx, url, options)
	if err == nil {
		t.Fatal(err)
	}

	if _, ok := err.(*DeadlineExceeded); !ok {
		t.Fatal("Expected error to be of type *DeadlineExceeded")
	}

	prefix := "Download timeout exceeded for"
	suffix := "/testdata/data.txt'"

	if !strings.HasPrefix(err.Error(), prefix) || !strings.HasSuffix(err.Error(), suffix) {
		t.Fatalf("Expected prefix '%s' and suffix '%s' but got '%s'", prefix, suffix, err.Error())
	}
}
