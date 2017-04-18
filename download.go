package download

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const (
	defaultGoroutines = 10
	defaultDir        = "go-download"
)

// var (
// 	pool = &sync.Pool{
// 		New: func() interface{} {
// 			return new(File)
// 		},
// 	}
// )

// ConcurrencyFn ...
type ConcurrencyFn func(contentLength int64) int64

// File ...
type File struct {
	url           string
	dir           string
	contentLength int64
	readers       []io.ReadCloser
	concurencyFn  ConcurrencyFn
	io.Reader
}

// Open ...
func Open(url string, fn ConcurrencyFn) (*File, error) {

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	return OpenContext(ctx, url, fn)
}

// OpenContext ...
func OpenContext(ctx context.Context, url string, fn ConcurrencyFn) (*File, error) {

	if fn == nil {
		fn = defaultConcurrencyFunc
	}

	// f := pool.Get().(*File)
	// f.url = url
	// f.concurencyFn = fn
	f := &File{
		url:          url,
		concurencyFn: fn,
	}

	resp, err := http.Head(f.url)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Invalid response code '%d'", resp.StatusCode)
	}

	f.contentLength = resp.ContentLength

	if t := resp.Header.Get("Accept-Ranges"); t == "bytes" {
		err = f.downloadRangeBytes(ctx)
	} else {
		err = f.download(ctx)
	}

	if err != nil {
		return nil, err
	}

	return f, nil
}

func (f *File) download(ctx context.Context) error {

	req, err := http.NewRequest(http.MethodGet, f.url, nil)
	if err != nil {
		return err
	}

	req = req.WithContext(ctx)

	var client http.Client

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Invalid response code '%d'", resp.StatusCode)
	}

	f.dir, err = ioutil.TempDir("", defaultDir)
	if err != nil {
		return err
	}

	fh, err := ioutil.TempFile(f.dir, "")
	if err != nil {
		return err
	}

	if cap(f.readers) > 0 {
		f.readers = append(f.readers, fh)
	} else {
		f.readers = []io.ReadCloser{fh}
	}

	_, err = io.Copy(fh, resp.Body)
	if err != nil {
		return err
	}

	fh.Seek(0, 0)

	f.Reader = fh

	return nil
}

func (f *File) downloadRangeBytes(ctx context.Context) error {

	if f.contentLength < 0 {
		return fmt.Errorf("Invalid content length '%d'", f.contentLength)
	}

	var err error
	var resume bool

	f.dir = filepath.Join(os.TempDir(), defaultDir+f.generateSHA1())

	if _, err = os.Stat(f.dir); os.IsNotExist(err) {
		err = os.Mkdir(f.dir, 0770) // only owner and group have RWX access
		if err != nil {
			return err
		}
	} else {
		resume = true
	}

	gorountines := f.concurencyFn(f.contentLength)
	chunkSize := f.contentLength / gorountines
	remainer := f.contentLength % chunkSize
	var pos int64
	var i int64

	chunkSize--

	// make readers array equal to # goroutines
	// done this way to allow for recycling of *File
	if int64(cap(f.readers)) < gorountines {
		f.readers = make([]io.ReadCloser, gorountines, gorountines)
	} else {
		f.readers = f.readers[:gorountines]
	}

	type result struct {
		idx int64
		r   io.ReadCloser
		err error
	}

	ch := make(chan result)
	wg := new(sync.WaitGroup)

	go func() {
		<-ctx.Done() // using just in case, however unlikely, the goroutines finish prior to scheduling all of them
		wg.Wait()
		close(ch)
	}()

	for ; i < gorountines; i++ {

		wg.Add(1)

		if i == gorountines-1 {
			chunkSize += remainer // add remainer to last download
		}

		go func(ctx context.Context, resume bool, idx, start, end int64, wg *sync.WaitGroup, ch chan<- result) {

			defer wg.Done()

			fPath := filepath.Join(f.dir, strconv.FormatInt(idx, 10))

			var fh *os.File
			var err error

			if resume {
				fi, err := os.Stat(fPath)
				if err != nil {
					if os.IsNotExist(err) {
						fh, err = os.Create(fPath)
					}
				}

				// file exists...must check if partial
				if fi.Size() < (end-start)+1 {

					// lets append/download only the bytes necessary
					start += fi.Size()

					fh, err = os.OpenFile(fPath, os.O_RDWR|os.O_APPEND, 0770)
				} else {

					fh, err = os.Open(fPath)
					if err != nil {
						select {
						case <-ctx.Done():
						case ch <- result{idx: idx, err: err}:
						}
						return
					}

					select {
					case <-ctx.Done():
					case ch <- result{idx: idx, r: fh}:
					}
					return
				}
			} else {
				fh, err = os.Create(fPath)
			}

			if err != nil {
				select {
				case <-ctx.Done():
				case ch <- result{idx: idx, err: err}:
				}
				return
			}

			var client http.Client

			req, err := http.NewRequest(http.MethodGet, f.url, nil)
			if err != nil {
				select {
				case <-ctx.Done():
				case ch <- result{idx: idx, err: err}:
				}
				return
			}

			req = req.WithContext(ctx)

			req.Header.Add("Range", fmt.Sprintf("bytes=%d-%d", start, end))

			resp, err := client.Do(req)
			if err != nil {
				select {
				case <-ctx.Done():
				case ch <- result{idx: idx, err: err}:
				}
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusPartialContent {
				select {
				case <-ctx.Done():
				case ch <- result{idx: idx, err: fmt.Errorf("Invalid response code '%d'", resp.StatusCode)}:
				}
				return
			}

			_, err = io.Copy(fh, resp.Body)
			if err != nil {
				select {
				case <-ctx.Done():
				case ch <- result{idx: idx, err: err}:
				}
				return
			}

			fh.Seek(0, 0)

			select {
			case <-ctx.Done():
			case ch <- result{idx: idx, r: fh}:
			}

		}(ctx, resume, i, pos, pos+chunkSize, wg, ch)

		pos += chunkSize + 1
	}

	var j int

FOR:
	for {
		select {
		case <-ctx.Done():
			err := ctx.Err()

			if err == context.Canceled {
				return fmt.Errorf("Cancelled download of '%s'", f.url)
			}

			// context.DeadlineExceeded
			return fmt.Errorf("Download timed out for '%s'", f.url)
		case res := <-ch:

			j++

			if res.err != nil {
				return res.err
			}

			f.readers[res.idx] = res.r

			if j == len(f.readers) {
				break FOR
			}
		}
	}

	readers := make([]io.Reader, len(f.readers))
	for i = 0; i < int64(len(f.readers)); i++ {
		readers[i] = f.readers[i]
	}

	f.Reader = io.MultiReader(readers...)

	return nil
}

// ContentLength ...
func (f *File) ContentLength() int64 {
	return f.contentLength
}

// Close ...
func (f *File) Close() error {

	// close readers from Download function
	for i := 0; i < len(f.readers); i++ {
		if f.readers[i] != nil { // possible if cancelled
			f.readers[i].Close()
		}
	}

	// f.readers = f.readers[0:0]
	// pool.Put(f)

	return os.RemoveAll(f.dir)
}

func (f *File) generateSHA1() string {

	h := sha1.New()
	io.WriteString(h, f.url)

	return fmt.Sprintf("%x", h.Sum(nil))
}

// chunks up downloads into 2MB chunks, when Accept-Ranges supported
func defaultConcurrencyFunc(length int64) int64 {
	return defaultGoroutines
}
