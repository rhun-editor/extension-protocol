package rhunext

// Custom editors (feature "custom-editors", v1.1/v1.1.md): the extension shows a kind of file in a
// view rhun opens for it. A text model edits rhun's document (the CDOC is its document id: Text and
// Replace work on it); an owned model keeps the file itself, reads it with EditorRead, says whether
// it differs from the file with EditorDirty and gives rhun the bytes in parts when it saves.

import (
	"errors"
	"fmt"
	"strconv"
)

// FeatureCustomEditors is the feature name to pass to Run.
const FeatureCustomEditors = "custom-editors"

// EditorPartMax is the most bytes an editor-read answer and an editor-save part carry.
const EditorPartMax = 4 << 20

// EditorOpen is editor-open: the file rhun opened in the extension's editor and the view it opened
// for the tab (draw it with Draw).
type EditorOpen struct {
	ID     int64
	CDoc   Doc
	View   View
	Model  string // "text" or "owned"
	Source string // "file"
	Path   string
}

// EditorSave is editor-save: KIND "save", "saveas" or "backup", PART from 0.
type EditorSave struct {
	ID   int64
	CDoc Doc
	Kind string
	Part int64
}

// CustomEditor opens files in its editor and saves an owned model's bytes part by part (whether
// another part follows, and this one: at most EditorPartMax; EditorPartOf cuts them). An error
// from EditorOpen closes the tab and opens the file as text.
type CustomEditor interface {
	EditorOpen(ext *Ext, req *EditorOpen) error
	EditorSave(ext *Ext, req *EditorSave) (more bool, part []byte, err error)
}

// EditorCommands takes rhun's Undo, Redo and Revert File in an owned model's tab (optional).
type EditorCommands interface {
	EditorUndo(ext *Ext, cdoc Doc) error
	EditorRedo(ext *Ext, cdoc Doc) error
	EditorRevert(ext *Ext, cdoc Doc) error
}

// EditorEvents hears that an owned model's file changed on disk, a Save As's new path and the
// tab's closing (optional).
type EditorEvents interface {
	EditorChanged(ext *Ext, cdoc Doc)
	EditorPath(ext *Ext, cdoc Doc, path string)
	EditorClosed(ext *Ext, cdoc Doc)
}

// EditorPartOf is part `part` of data in parts of size bytes, and whether another follows.
func EditorPartOf(data []byte, part int64, size int) (bool, []byte) {
	if size < 1 {
		size = 1
	}
	if size > EditorPartMax {
		size = EditorPartMax
	}
	start := int(part) * size
	if part < 0 || start > len(data) {
		start = len(data)
	}
	end := start + size
	if end > len(data) {
		end = len(data)
	}
	return end < len(data), data[start:end]
}

func (e *Ext) editorAgreed() error {
	if !e.Has(FeatureCustomEditors) {
		return &Error{Code: "unknown", Text: "custom-editors was not agreed"}
	}
	return nil
}

// EditorRead returns up to max bytes (at most EditorPartMax) of the editor's file from offset;
// none at its end.
func (e *Ext) EditorRead(cdoc Doc, offset int64, max int) ([]byte, error) {
	if err := e.editorAgreed(); err != nil {
		return nil, err
	}
	if max > EditorPartMax {
		max = EditorPartMax
	}
	r, err := e.Request("editor-read", nil, itoa(int64(cdoc)), itoa(offset), itoa(int64(max)))
	return r.Body, err
}

// EditorReadAll reads the whole file in parts of 4 MB.
func (e *Ext) EditorReadAll(cdoc Doc) ([]byte, error) {
	var data []byte
	for {
		part, err := e.EditorRead(cdoc, int64(len(data)), EditorPartMax)
		if err != nil {
			return data, err
		}
		if len(part) == 0 {
			return data, nil
		}
		data = append(data, part...)
	}
}

// EditorDirty tells whether an owned model differs from its file: the dirty dot and the close
// question.
func (e *Ext) EditorDirty(cdoc Doc, dirty bool) error {
	if err := e.editorAgreed(); err != nil {
		return err
	}
	d := "0"
	if dirty {
		d = "1"
	}
	_, err := e.Request("editor-dirty", nil, itoa(int64(cdoc)), d)
	return err
}

// answerEditor answers editor-open, editor-save, editor-undo, -redo and -revert; false for any
// other request.
func (e *Ext) answerEditor(h Handler, f []string, body []byte) bool {
	if len(f) < 3 {
		return false
	}
	switch f[1] {
	case "editor-open", "editor-save", "editor-undo", "editor-redo", "editor-revert":
	default:
		return false
	}
	id := f[0]
	c, is := h.(CustomEditor)
	if !is || !e.Has(FeatureCustomEditors) {
		e.write(fmt.Sprintf("error %s unknown no such request\n", id), nil)
		return true
	}
	rid, _ := strconv.ParseInt(id, 10, 64)
	cdoc, _ := strconv.ParseInt(f[2], 10, 64)
	var out []byte
	var err error
	switch f[1] {
	case "editor-open":
		if len(f) != 7 {
			err = errors.New("editor-open takes CDOC VIEW MODEL SOURCE LEN")
			break
		}
		view, _ := strconv.ParseInt(f[3], 10, 64)
		err = c.EditorOpen(e, &EditorOpen{ID: rid, CDoc: Doc(cdoc), View: View(view), Model: f[4], Source: f[5], Path: string(body)})
	case "editor-save":
		if len(f) != 5 {
			err = errors.New("editor-save takes CDOC KIND PART")
			break
		}
		part, _ := strconv.ParseInt(f[4], 10, 64)
		var more bool
		var bytes []byte
		more, bytes, err = c.EditorSave(e, &EditorSave{ID: rid, CDoc: Doc(cdoc), Kind: f[3], Part: part})
		if err == nil && len(bytes) > EditorPartMax {
			err = errors.New("a part over 4 MB")
		}
		if err == nil {
			out = append([]byte("0\n"), bytes...)
			if more {
				out[0] = '1'
			}
		}
	default:
		if cmds, ok := h.(EditorCommands); ok {
			switch f[1] {
			case "editor-undo":
				err = cmds.EditorUndo(e, Doc(cdoc))
			case "editor-redo":
				err = cmds.EditorRedo(e, Doc(cdoc))
			default:
				err = cmds.EditorRevert(e, Doc(cdoc))
			}
		}
	}
	if err != nil {
		e.write(errorLine(id, &Error{Code: "refused", Text: err.Error()}), nil)
	} else {
		e.write(fmt.Sprintf("ok %s %d\n", id, len(out)), out)
	}
	return true
}

// editorEvent delivers editor-changed, editor-path and editor-closed; false for any other event.
func (e *Ext) editorEvent(h Handler, ev Event) bool {
	if !e.Has(FeatureCustomEditors) {
		return false
	}
	switch ev.Name {
	case "editor-changed", "editor-closed", "editor-path":
	default:
		return false
	}
	x, is := h.(EditorEvents)
	if !is || len(ev.Args) < 1 {
		return false
	}
	cdoc, _ := strconv.ParseInt(ev.Args[0], 10, 64)
	switch ev.Name {
	case "editor-changed":
		x.EditorChanged(e, Doc(cdoc))
	case "editor-closed":
		x.EditorClosed(e, Doc(cdoc))
	default:
		x.EditorPath(e, Doc(cdoc), string(ev.Body))
	}
	return true
}
