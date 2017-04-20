package download

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
)

func BenchmarkDownload(b *testing.B) {

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

	b.ResetTimer()

	for n := 0; n < b.N; n++ {

		f, err := Open(url, nil)
		if err != nil {
			b.Fatal(err)
		}
		f.Close()
	}
}

func BenchmarkDownloadWithRange(b *testing.B) {

	fs := http.StripPrefix("/testdata/", http.FileServer(http.Dir("./testdata")))

	mux := http.NewServeMux()
	mux.Handle("/testdata/", fs)

	server := httptest.NewServer(mux)
	defer server.Close()

	url := server.URL + "/testdata/data.txt"

	b.ResetTimer()

	for n := 0; n < b.N; n++ {

		f, err := Open(url, nil)
		if err != nil {
			b.Fatal(err)
		}
		f.Close()
	}
}
