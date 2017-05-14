package main

import (
	"fmt"
	"io"
	"log"

	download "github.com/joeybloggs/go-download"
	"github.com/vbauerster/mpb"
)

func main() {

	progress := mpb.New().SetWidth(80)
	defer progress.Stop()

	options := &download.Options{
		Proxy: func(name string, download int, size int64, r io.Reader) io.Reader {
			bar := progress.AddBar(size).
				PrependName(fmt.Sprintf("%s-%d", name, download), 0, 0).
				PrependCounters("%3s / %3s", mpb.UnitBytes, 18, mpb.DwidthSync|mpb.DextraSpace).
				AppendPercentage(5, 0)

			return bar.ProxyReader(r)
		},
	}

	f, err := download.Open("https://storage.googleapis.com/golang/go1.8.1.src.tar.gz", options)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	// f implements io.Reader, write file somewhere or do some other sort of work with it
}
