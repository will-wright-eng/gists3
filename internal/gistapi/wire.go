package gistapi

import "time"

// GitHub JSON wire shapes, quarantined here so the public API never leaks
// them. If GitHub changes a field, this file is the blast radius.

type Gist struct {
	ID          string              `json:"id"`
	Description string              `json:"description"`
	Public      bool                `json:"public"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	Files       map[string]GistFile `json:"files"`
}

type GistFile struct {
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
	RawURL    string `json:"raw_url"`
}

// GistCreate is the POST /gists request body.
type GistCreate struct {
	Description string               `json:"description,omitempty"`
	Public      bool                 `json:"public"`
	Files       map[string]*FileEdit `json:"files"`
}

// GistPatch is the PATCH /gists/{id} request body. A nil *FileEdit marshals
// to JSON null, which tells GitHub to delete that file.
type GistPatch struct {
	Files map[string]*FileEdit `json:"files"`
}

type FileEdit struct {
	Content string `json:"content"`
}
