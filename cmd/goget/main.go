package main

import (
	"fmt"
	"io"
	"log"

	"os"

	download "github.com/joeybloggs/go-download"
	"github.com/vbauerster/mpb"
)

func main() {

	url := os.Args[len(os.Args)-1]

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

	f, err := download.Open(url, options)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		log.Fatal(err)
	}

	var output *os.File
	name := info.Name()
	output, err = os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		log.Fatal(err)
	}
	defer output.Close()

	if _, err = io.Copy(output, f); err != nil {
		log.Fatal(err)
	}

	log.Printf("Success. %s saved.", name)
}
