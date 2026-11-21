// Package filetemplates stores user-authored global local-only template resources.
package filetemplates

import "errors"

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type Template struct {
	ID          string `json:"id"`
	Revision    uint64 `json:"revision"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	DefaultName string `json:"defaultName"`
	Files       []File `json:"files"`
}
type Document struct {
	Version   int        `json:"version"`
	Revision  uint64     `json:"revision"`
	Templates []Template `json:"templates"`
}
type Saved struct {
	Revision uint64   `json:"revision"`
	Template Template `json:"template"`
}
type FileSummary struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Size   int    `json:"size"`
}
type Summary struct {
	ID          string        `json:"id"`
	Revision    uint64        `json:"revision"`
	Kind        string        `json:"kind"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Category    string        `json:"category"`
	DefaultName string        `json:"defaultName"`
	Files       []FileSummary `json:"files"`
}
type Snapshot struct {
	Version   int       `json:"version"`
	Revision  uint64    `json:"revision"`
	Templates []Summary `json:"templates"`
}

// Required string fields must not silently turn missing/null stored data into empty files.
func (f *File) UnmarshalJSON(raw []byte) error {
	var fields struct {
		Path    *string `json:"path"`
		Content *string `json:"content"`
	}
	if err := strict(raw, &fields); err != nil {
		return err
	}
	if fields.Path == nil || fields.Content == nil {
		return errors.New("file_template_invalid_file")
	}
	f.Path, f.Content = *fields.Path, *fields.Content
	return nil
}
