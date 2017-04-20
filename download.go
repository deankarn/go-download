package download

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	defaultGoroutines = 10
	defaultDir        = "go-download"
)

var (
	_           io.Reader = (*File)(nil)
	fileMode              = os.FileMode(0770)
	defaultTime           = time.Time{}
)

// Options contains any specific configuration values
// for downloading/opening a file
type Options struct {
	Concurrency ConcurrencyFn
	Proxy       ProxyFn
}

// ConcurrencyFn is the function used to determine the level of concurrency aka the
// number of goroutines to use. Default concurrency level is 10
//
// if returned value is < 1 then the default value will be used
type ConcurrencyFn func(size int64) int

// ProxyFn is the function used to pass the download io.Reader for proxying.
// eg. displaying a progress bar of the download.
type ProxyFn func(name string, size int64, r io.Reader) io.Reader

// File represents an open file descriptor to a downloaded file(s)
type File struct {
	url     string
	dir     string
	size    int64
	modTime time.Time
	options *Options
	readers []io.ReadCloser
	io.Reader
}

type partialResult struct {
	idx int
	r   io.ReadCloser
	err error
}

// Open downloads and opens the file(s) downloaded by the given url
func Open(url string, options *Options) (*File, error) {

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	return OpenContext(ctx, url, options)
}

// OpenContext downloads and opens the file(s) downloaded by the given url and is cancellable using the provided context.
// The context provided must be non-nil
func OpenContext(ctx context.Context, url string, options *Options) (*File, error) {

	if ctx == nil {
		panic("nil context")
	}

	f := &File{
		url:     url,
		options: options,
	}

	resp, err := http.Head(f.url)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &InvalidResponseCode{got: resp.StatusCode, expected: http.StatusOK}
	}

	f.size = resp.ContentLength

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
		return &InvalidResponseCode{got: resp.StatusCode, expected: http.StatusOK}
	}

	f.dir, err = ioutil.TempDir("", defaultDir)
	if err != nil {
		return err
	}

	fh, err := ioutil.TempFile(f.dir, "")
	if err != nil {
		return err
	}

	f.readers = make([]io.ReadCloser, 1)
	f.readers[0] = fh

	var read io.Reader = resp.Body

	if f.options != nil && f.options.Proxy != nil {
		read = f.options.Proxy(filepath.Base(f.url), f.size, read)
	}

	_, err = io.Copy(fh, read)
	if err != nil {
		return err
	}

	fh.Seek(0, 0)

	f.Reader = fh
	f.modTime = time.Now()

	return nil
}

func (f *File) downloadRangeBytes(ctx context.Context) error {

	if f.size <= 0 {
		return fmt.Errorf("Invalid content length '%d'", f.size)
	}

	var err error
	var resume bool

	f.dir = filepath.Join(os.TempDir(), defaultDir+f.generateHash())

	if _, err = os.Stat(f.dir); os.IsNotExist(err) {
		err = os.Mkdir(f.dir, fileMode) // only owner and group have RWX access
		if err != nil {
			return err
		}
	} else {
		resume = true
	}

	var goroutines int

	if f.options == nil || f.options.Concurrency == nil {
		goroutines = defaultConcurrencyFunc(f.size)
	} else {
		goroutines = f.options.Concurrency(f.size)
		if goroutines < 1 {
			goroutines = defaultConcurrencyFunc(f.size)
		}
	}

	chunkSize := f.size / int64(goroutines)
	remainer := f.size % chunkSize
	var pos int64

	chunkSize--

	f.readers = make([]io.ReadCloser, goroutines, goroutines)

	ch := make(chan partialResult)
	wg := new(sync.WaitGroup)
	wg.Add(goroutines)

	go func() {
		<-ctx.Done() // using just in case, however unlikely, the goroutines finish prior to scheduling all of them
		wg.Wait()
		close(ch)
	}()

	var i int

	for ; i < goroutines; i++ {

		if i == goroutines-1 {
			chunkSize += remainer // add remainer to last download
		}

		go f.downloadPartial(ctx, resume, i, pos, pos+chunkSize, wg, ch)

		pos += chunkSize + 1
	}

	var j int

FOR:
	for {
		select {
		case <-ctx.Done():
			err := ctx.Err()

			if err == context.Canceled {
				return &Canceled{url: f.url}
			}

			// context.DeadlineExceeded
			return &DeadlineExceeded{url: f.url}
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
	for i = 0; i < len(f.readers); i++ {
		readers[i] = f.readers[i]
	}

	f.Reader = io.MultiReader(readers...)
	f.modTime = time.Now()

	return nil
}

func (f *File) downloadPartial(ctx context.Context, resumeable bool, idx int, start, end int64, wg *sync.WaitGroup, ch chan<- partialResult) {

	defer wg.Done()

	fPath := filepath.Join(f.dir, strconv.Itoa(idx))

	var fh *os.File
	var err error

	if resumeable {
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

			fh, err = os.OpenFile(fPath, os.O_RDWR|os.O_APPEND, fileMode)
		} else {

			fh, err = os.Open(fPath)
			if err != nil {
				select {
				case <-ctx.Done():
				case ch <- partialResult{idx: idx, err: err}:
				}
				return
			}

			select {
			case <-ctx.Done():
			case ch <- partialResult{idx: idx, r: fh}:
			}
			return
		}
	} else {
		fh, err = os.Create(fPath)
	}

	if err != nil {
		select {
		case <-ctx.Done():
		case ch <- partialResult{idx: idx, err: err}:
		}
		return
	}

	var client http.Client

	req, err := http.NewRequest(http.MethodGet, f.url, nil)
	if err != nil {
		select {
		case <-ctx.Done():
		case ch <- partialResult{idx: idx, err: err}:
		}
		return
	}

	req = req.WithContext(ctx)

	req.Header.Add("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := client.Do(req)
	if err != nil {
		select {
		case <-ctx.Done():
		case ch <- partialResult{idx: idx, err: err}:
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		select {
		case <-ctx.Done():
		case ch <- partialResult{idx: idx, err: &InvalidResponseCode{got: resp.StatusCode, expected: http.StatusPartialContent}}:
		}
		return
	}

	var read io.Reader = resp.Body

	if f.options != nil && f.options.Proxy != nil {
		read = f.options.Proxy(fmt.Sprintf("%s-%d", filepath.Base(f.url), idx), (end-start)+1, read)
	}

	_, err = io.Copy(fh, read)
	if err != nil {
		select {
		case <-ctx.Done():
		case ch <- partialResult{idx: idx, err: err}:
		}
		return
	}

	fh.Seek(0, 0)

	select {
	case <-ctx.Done():
	case ch <- partialResult{idx: idx, r: fh}:
	}
}

// Stat returns the FileInfo structure describing file(s). If there is an error, it will be of type *PathError.
func (f *File) Stat() (os.FileInfo, error) {

	if f.modTime.IsZero() {
		return nil, &os.PathError{Op: "stat", Path: filepath.Base(f.url), Err: errors.New("bad file descriptor")}
	}

	return &fileInfo{
		name:    filepath.Base(f.url),
		size:    f.size,
		mode:    fileMode,
		modTime: f.modTime,
	}, nil
}

// Close closes the File(s), rendering it unusable for I/O. It returns an error, if any.
func (f *File) Close() error {

	// close readers from Download function
	for i := 0; i < len(f.readers); i++ {
		if f.readers[i] != nil { // possible if cancelled
			f.readers[i].Close()
		}
	}

	f.modTime = defaultTime

	return os.RemoveAll(f.dir)
}

func (f *File) generateHash() string {

	// Open to a better way, but should not collide
	h := sha1.New()
	io.WriteString(h, f.url)

	return fmt.Sprintf("%x", h.Sum(nil))
}

// chunks up downloads into 2MB chunks, when Accept-Ranges supported
func defaultConcurrencyFunc(length int64) int {
	return defaultGoroutines
}
