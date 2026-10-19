package rhunext

// Feature "files" (needs project = read): find-files lists the workspace's files by glob,
// search-text searches their text, through rhun (git's ignore rules where git runs, the exclude
// setting, unsaved editor text). Results come in batches while the request runs.

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

const (
	FeatureFiles = "files"
	// FileQuery.Flags
	FilesIgnored     = 1  // ignored files too: no .gitignore, no exclude setting
	FilesSkipHidden  = 2  // skip names starting with .
	FilesFollowLinks = 4  // follow links to folders
	FilesFolders     = 8  // find-files: folders too, with a trailing /
	SearchIgnoreCase = 16 // search-text: Unicode simple case folding
	SearchWholeWord  = 32
	searchRegex      = 64
)

// FileQuery says where to look: Folder is a folder id (1 the primary) or 0 for all; Max is 1 to
// 100000 paths (find) or 1 to 20000 matches (search); Include and Exclude are globs relative to
// the folder (* ** ? [..] {a,b}; without / they match the base name).
type FileQuery struct {
	Folder  int64
	Max     int
	Flags   int
	Include []string
	Exclude []string
}

func (q FileQuery) globs() ([]byte, error) {
	var b bytes.Buffer
	for _, g := range q.Include {
		if g == "" || strings.ContainsAny(g, "\r\n") {
			return nil, &Error{Code: "syntax", Text: "an empty glob or one with a line break"}
		}
		b.WriteString("+" + g + "\n")
	}
	for _, g := range q.Exclude {
		if g == "" || strings.ContainsAny(g, "\r\n") {
			return nil, &Error{Code: "syntax", Text: "an empty glob or one with a line break"}
		}
		b.WriteString("-" + g + "\n")
	}
	return b.Bytes(), nil
}

// FoundPath is one find-files record; a folder's Path ends with /.
type FoundPath struct {
	Folder int64
	Path   string
}

// SearchMatch is one match: Col to End is the half-open byte range on Line (one-based); Text is
// the line, or 512 bytes of a long one, whose first byte is at column Pre.
type SearchMatch struct {
	Line, Col, End, Pre int
	Text                string
}

// FoundFile is a file's matches; Doc and Version are the open document that was searched, or 0.
type FoundFile struct {
	Folder  int64
	Doc     Doc
	Version int64
	Path    string
	Matches []SearchMatch
}

// Pattern is a literal (1 to 4096 bytes without LF) or, when Program is set, an RNRX program
// (CompileSearchRegex).
type Pattern struct {
	Literal string
	Program []byte
}

// Finished is a run's reply: Count paths or matches, Files with matches, Skipped (too big or
// binary), Status complete, limit or partial.
type Finished struct {
	Count, Files, Skipped int64
	Status                string
}

// CompileSearchRegex compiles a regex (the snippet transforms' grammar; options such as "i") to
// the RNRX program search-text takes.
func CompileSearchRegex(source, options string) ([]byte, error) {
	r, err := compileSnippetRegex(source, options)
	if err != nil {
		return nil, err
	}
	return r.encode()
}

// FindFilesContext lists files; onBatch gets each batch as it comes and returns false to stop
// the run (it is cancelled, and the error has code "cancelled").
func (e *Ext) FindFilesContext(ctx context.Context, q FileQuery, onBatch func([]FoundPath) bool) (Finished, error) {
	if !e.Has(FeatureFiles) {
		return Finished{}, notAgreed(FeatureFiles)
	}
	g, err := q.globs()
	if err != nil {
		return Finished{}, err
	}
	r, err := e.runFiles(ctx, "find-files", q, g, func(b []byte) (bool, error) {
		paths, err := ParseFoundPaths(b)
		if err != nil {
			return false, err
		}
		return onBatch(paths), nil
	})
	if err != nil {
		return Finished{}, err
	}
	if len(r.Args) != 2 {
		return Finished{}, &Error{Code: "syntax", Text: "invalid find-files answer"}
	}
	n, _ := strconv.ParseInt(r.Args[0], 10, 64)
	return Finished{Count: n, Status: r.Args[1]}, nil
}

// SearchTextContext searches the files' text (an open document's where there is one).
func (e *Ext) SearchTextContext(ctx context.Context, p Pattern, q FileQuery, onBatch func([]FoundFile) bool) (Finished, error) {
	if !e.Has(FeatureFiles) {
		return Finished{}, notAgreed(FeatureFiles)
	}
	g, err := q.globs()
	if err != nil {
		return Finished{}, err
	}
	pat := []byte(p.Literal)
	if p.Program != nil {
		pat = p.Program
		q.Flags |= searchRegex
	} else if len(pat) == 0 || len(pat) > 4096 || bytes.IndexByte(pat, '\n') >= 0 {
		return Finished{}, &Error{Code: "syntax", Text: "a literal is 1 to 4096 bytes without LF"}
	}
	body := append([]byte(fmt.Sprintf("%d %d\n", len(pat), len(g))), pat...)
	body = append(body, g...)
	r, err := e.runFiles(ctx, "search-text", q, body, func(b []byte) (bool, error) {
		files, err := ParseFoundFiles(b)
		if err != nil {
			return false, err
		}
		return onBatch(files), nil
	})
	if err != nil {
		return Finished{}, err
	}
	if len(r.Args) != 4 {
		return Finished{}, &Error{Code: "syntax", Text: "invalid search-text answer"}
	}
	var f Finished
	f.Count, _ = strconv.ParseInt(r.Args[0], 10, 64)
	f.Files, _ = strconv.ParseInt(r.Args[1], 10, 64)
	f.Skipped, _ = strconv.ParseInt(r.Args[2], 10, 64)
	f.Status = r.Args[3]
	return f, nil
}

// runFiles sends the request and hands each batch to batch until the reply; batch returning false
// (or ctx ending) cancels the run.
func (e *Ext) runFiles(ctx context.Context, verb string, q FileQuery, body []byte, batch func([]byte) (bool, error)) (Reply, error) {
	stream := make(chan []byte, 1024)
	id, ch, err := e.startRequest(ctx, verb, body, stream, itoa(q.Folder), itoa(int64(q.Max)), itoa(int64(q.Flags)),
		itoa(int64(len(body))))
	if err != nil {
		return Reply{}, err
	}
	defer func() {
		e.pmu.Lock()
		delete(e.streams, id)
		e.pmu.Unlock()
	}()
	cancelled := false
	cancel := func() {
		if !cancelled {
			cancelled = true
			go e.RequestContext(context.Background(), "cancel", nil, itoa(id))
		}
	}
	var batchErr error
	take := func(b []byte) {
		if cancelled || batchErr != nil {
			return
		}
		more, err := batch(b)
		if err != nil {
			batchErr = err
			cancel()
		} else if !more {
			cancel()
		}
	}
	done := ctx.Done()
	for {
		select {
		case b := <-stream:
			take(b)
		case res := <-ch:
			// batches come before the reply: take those still waiting
			for {
				select {
				case b := <-stream:
					take(b)
					continue
				default:
				}
				break
			}
			if batchErr != nil {
				return Reply{}, batchErr
			}
			return res.reply, res.err
		case <-done:
			cancel()
			done = nil
		case <-e.done:
			e.cancelPending(id, ch, false)
			return Reply{}, e.connectionError()
		}
	}
}

// ParseFoundPaths reads a find-files batch: N LF, then N records FOLDER PATH_LEN LF PATH.
func ParseFoundPaths(b []byte) ([]FoundPath, error) {
	r := framedReader{b: b}
	n, err := r.count()
	if err != nil {
		return nil, err
	}
	out := make([]FoundPath, 0, n)
	for i := 0; i < n; i++ {
		f, err := r.header(2)
		if err != nil {
			return nil, err
		}
		path, err := r.bytes(int(f[1]))
		if err != nil {
			return nil, err
		}
		out = append(out, FoundPath{Folder: f[0], Path: string(path)})
	}
	if r.at != len(b) {
		return nil, fmt.Errorf("found: %d bytes after the records", len(b)-r.at)
	}
	return out, nil
}

// ParseFoundFiles reads a search-text batch: N LF, then N groups FOLDER DOC VERSION PATH_LEN M LF
// PATH, each followed by M matches LINE COL END PRE TEXT_LEN LF TEXT.
func ParseFoundFiles(b []byte) ([]FoundFile, error) {
	r := framedReader{b: b}
	n, err := r.count()
	if err != nil {
		return nil, err
	}
	out := make([]FoundFile, 0, n)
	for i := 0; i < n; i++ {
		f, err := r.header(5)
		if err != nil {
			return nil, err
		}
		path, err := r.bytes(int(f[3]))
		if err != nil {
			return nil, err
		}
		file := FoundFile{Folder: f[0], Doc: Doc(f[1]), Version: f[2], Path: string(path)}
		for j := int64(0); j < f[4]; j++ {
			m, err := r.header(5)
			if err != nil {
				return nil, err
			}
			text, err := r.bytes(int(m[4]))
			if err != nil {
				return nil, err
			}
			file.Matches = append(file.Matches, SearchMatch{Line: int(m[0]), Col: int(m[1]), End: int(m[2]),
				Pre: int(m[3]), Text: string(text)})
		}
		out = append(out, file)
	}
	if r.at != len(b) {
		return nil, fmt.Errorf("found: %d bytes after the records", len(b)-r.at)
	}
	return out, nil
}

type framedReader struct {
	b  []byte
	at int
}

func (r *framedReader) line() (string, error) {
	i := bytes.IndexByte(r.b[r.at:], '\n')
	if i < 0 {
		return "", fmt.Errorf("found: a record without its line")
	}
	s := string(r.b[r.at : r.at+i])
	r.at += i + 1
	return s, nil
}

func (r *framedReader) count() (int, error) {
	s, err := r.line()
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 100000 {
		return 0, fmt.Errorf("found: %q is not a record count", s)
	}
	return n, nil
}

func (r *framedReader) header(fields int) ([]int64, error) {
	s, err := r.line()
	if err != nil {
		return nil, err
	}
	f := strings.Split(s, " ")
	if len(f) != fields {
		return nil, fmt.Errorf("found: %q has not %d fields", s, fields)
	}
	out := make([]int64, fields)
	for i, x := range f {
		v, err := strconv.ParseInt(x, 10, 64)
		if err != nil || v < 0 {
			return nil, fmt.Errorf("found: %q is not a number", x)
		}
		out[i] = v
	}
	return out, nil
}

func (r *framedReader) bytes(n int) ([]byte, error) {
	if n < 0 || r.at+n > len(r.b) {
		return nil, fmt.Errorf("found: a length past the batch")
	}
	b := r.b[r.at : r.at+n]
	r.at += n
	return b, nil
}
