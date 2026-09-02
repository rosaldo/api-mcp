package spec

import (
	"strings"
)

// InstructionsLimit is how much of the server instructions a client keeps. Claude Code truncates
// them at 2KB, so anything past that is written for nobody. Put what matters first.
const InstructionsLimit = 2048

// Instructions is what the server says about itself before a single tool is loaded.
//
// It matters more than it looks. Clients that support tool search — the default in Claude Code —
// load only tool NAMES and this text when the session opens; every description and every
// parameter schema stays deferred until the model goes looking for them. So this paragraph is
// what decides whether the tools are ever found at all: a server that says nothing is a server
// the model has no reason to search.
//
// The text comes from the spec's own `info`, because that is where an API already describes
// itself — nobody should have to write it twice. A schema with no `info` (GraphQL SDL, for one)
// yields an empty string, and the caller falls back to whatever it wants.
func (d *Document) Instructions() string {
	title, description := d.info()
	if title == "" && description == "" {
		return ""
	}
	var b strings.Builder
	if title != "" {
		b.WriteString(title)
	}
	if description != "" {
		if title != "" {
			b.WriteString(" — ")
		}
		b.WriteString(strings.Join(strings.Fields(description), " "))
	}
	return truncate(b.String(), InstructionsLimit)
}

// info pulls the title and description out of the raw document, wherever that dialect keeps them.
//
// It reads the bytes directly instead of asking the dialect: YAML is a superset of JSON, so one
// decoder covers both encodings. OpenAPI 3.x and Swagger 2.0 nest them under `info`; a Google
// discovery document puts them at the top level. Doing it here keeps every dialect's signature
// untouched.
func (d *Document) info() (title, description string) {
	var doc struct {
		Info struct {
			Title       string `yaml:"title" json:"title"`
			Description string `yaml:"description" json:"description"`
		} `yaml:"info" json:"info"`
		Title       string `yaml:"title" json:"title"`             // discovery
		Description string `yaml:"description" json:"description"` // discovery
	}
	if err := decode(d.Raw, &doc); err != nil {
		return "", "" // not YAML/JSON: the caller decides what to do
	}
	title = strings.TrimSpace(coalesce(doc.Info.Title, doc.Title))
	description = strings.TrimSpace(coalesce(doc.Info.Description, doc.Description))
	return title, description
}

func coalesce(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// truncate cuts on a word boundary when it can, so the text does not end mid-word.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexByte(cut, ' '); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:—-") + "…"
}
