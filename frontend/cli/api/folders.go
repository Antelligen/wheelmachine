package api

import (
	"context"
	"io"
	"iter"
	"net/http"
	"net/url"
)

// Team folders: files a team shares, on the dashboard and in every one of its machines at ~/folders, and each
// person's own, My files. Deleting puts in the folder's Trash for 30 days.

// SharedFolder is a team folder, or a person's own, as the caller may use it: edit, view, or size (its size alone).
type SharedFolder struct {
	Object    string        `json:"object"`
	ID        string        `json:"id"`
	Kind      string        `json:"kind"` // team or own
	Name      string        `json:"name"`
	Person    *PersonName   `json:"person"`
	LeftAt    *string       `json:"left_at"`
	GoesAt    *string       `json:"goes_at"`
	Access    string        `json:"access"`
	Teams     []FolderGrant `json:"teams"`
	Bytes     int64         `json:"bytes"`
	ChangedAt *string       `json:"changed_at"`
	MadeAt    string        `json:"made_at"`
}

// FolderGrant is a folder given to a team: to view, or to edit.
type FolderGrant struct {
	Object     string `json:"object"`
	Folder     string `json:"folder"`
	FolderName string `json:"folder_name"`
	Project    string `json:"project"`
	Access     string `json:"access"`
	GrantedAt  string `json:"granted_at"`
}

// FolderList is the folders the caller sees, with what the account keeps in them and may (to those who are told).
type FolderList struct {
	Object        string         `json:"object"`
	On            bool           `json:"on"`
	Data          []SharedFolder `json:"data"`
	UsedBytes     *int64         `json:"used_bytes,omitempty"`
	CapBytes      *int64         `json:"cap_bytes,omitempty"`
	FileMostBytes int64          `json:"file_most_bytes"`
}

// FolderEntry is a file or a folder in a folder.
type FolderEntry struct {
	Name      string      `json:"name"`
	Type      string      `json:"type"` // file or folder
	Size      int64       `json:"size"`
	ChangedAt string      `json:"changed_at"`
	ChangedBy *PersonName `json:"changed_by"`
}

// FolderListing is one page of what is in one folder of a folder: folders first, then files, each by name. Next is
// where the next page starts, nil after the last.
type FolderListing struct {
	Object  string        `json:"object"`
	Folder  string        `json:"folder"`
	Path    string        `json:"path"`
	Entries []FolderEntry `json:"entries"`
	Next    *string       `json:"next"`
}

// FolderTrash is what one deletion put in a folder's Trash, and when it goes for good.
type FolderTrash struct {
	Object    string      `json:"object"`
	ID        string      `json:"id"`
	Folder    string      `json:"folder"`
	Path      string      `json:"path"`
	Type      string      `json:"type"`
	Bytes     int64       `json:"bytes"`
	DeletedAt string      `json:"deleted_at"`
	DeletedBy *PersonName `json:"deleted_by"`
	GoesAt    string      `json:"goes_at"`
}

// Folders returns the folders the caller sees: their own first.
func (c *Client) Folders(ctx context.Context) (*FolderList, error) {
	var out FolderList
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/folders", timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListFolder returns one page of what is in a folder of a folder ("" its top): the first for after "", else the one
// that starts at the Next of the page before.
func (c *Client) ListFolder(ctx context.Context, folder, path, after string) (*FolderListing, error) {
	id, err := segment("folder", folder)
	if err != nil {
		return nil, err
	}
	q := url.Values{"path": {path}}
	if after != "" {
		q.Set("after", after)
	}
	var out FolderListing
	if err := c.decode(ctx, call{method: http.MethodGet, path: "/v1/folders/" + id + "/list", query: q, timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AllFolderEntries walks everything in a folder of a folder, folders first, asking for each page only when the one
// before it is used up. Stop early by breaking out of the loop.
func (c *Client) AllFolderEntries(ctx context.Context, folder, path string) iter.Seq2[FolderEntry, error] {
	return func(yield func(FolderEntry, error) bool) {
		after, seen := "", map[string]bool{}
		for {
			l, err := c.ListFolder(ctx, folder, path, after)
			if err != nil {
				yield(FolderEntry{}, err)
				return
			}
			for _, e := range l.Entries {
				if !yield(e, nil) {
					return
				}
			}
			if l.Next == nil || *l.Next == "" || seen[*l.Next] {
				return
			}
			seen[*l.Next] = true
			after = *l.Next
		}
	}
}

// ReadFolderFile returns a file of a folder, as a stream.
func (c *Client) ReadFolderFile(ctx context.Context, folder, path string) (*File, error) {
	id, err := segment("folder", folder)
	if err != nil {
		return nil, err
	}
	res, err := c.do(ctx, call{method: http.MethodGet, path: "/v1/folders/" + id + "/files", query: url.Values{"path": {path}},
		accept: "application/octet-stream"})
	if err != nil {
		return nil, err
	}
	return &File{ReadCloser: res.Body, Size: res.ContentLength}, nil
}

// WriteFolderFile writes size bytes from r to a file of a folder, in the place of the one there, making the folders on
// the way. The file is sent again after a failed try only when r can be wound back.
func (c *Client) WriteFolderFile(ctx context.Context, folder, path string, r io.Reader, size int64) (*FolderEntry, error) {
	id, err := segment("folder", folder)
	if err != nil {
		return nil, err
	}
	if size < 0 {
		return nil, &Error{Code: CodeInvalidRequest, Detail: "the file's length is needed to write it"}
	}
	if r == nil {
		r = http.NoBody
	}
	var out FolderEntry
	if err := c.decode(ctx, call{method: http.MethodPut, path: "/v1/folders/" + id + "/files", query: url.Values{"path": {path}}, stream: r,
		size: size}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteFolderFile puts a file or folder of a folder in its Trash, from which it can be put back for 30 days.
func (c *Client) DeleteFolderFile(ctx context.Context, folder, path string) (*FolderTrash, error) {
	id, err := segment("folder", folder)
	if err != nil {
		return nil, err
	}
	var out FolderTrash
	if err := c.decode(ctx, call{method: http.MethodDelete, path: "/v1/folders/" + id + "/files", query: url.Values{"path": {path}},
		timeout: callTimeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MoveFolderItem moves or renames a file or folder within a folder.
func (c *Client) MoveFolderItem(ctx context.Context, folder, from, to string) error {
	id, err := segment("folder", folder)
	if err != nil {
		return err
	}
	return c.decode(ctx, call{method: http.MethodPost, path: "/v1/folders/" + id + "/move", in: map[string]string{"from": from, "to": to},
		timeout: callTimeout}, nil)
}
