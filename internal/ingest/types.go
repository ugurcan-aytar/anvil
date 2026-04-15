package ingest

// Source is a single raw document ready for the extract stage. Callers
// (typically the ingest command) read the file once and populate all
// three fields — Extract does not touch the filesystem.
type Source struct {
	// Path is the file path relative to the project root. Used in
	// prompts and stored in page frontmatter so users can trace a
	// claim back to its origin.
	Path string
	// Title defaults to the filename stem; the ingest command may
	// upgrade this to the document's first H1 when one exists.
	Title string
	// Content is the text body fed to the LLM. Any YAML frontmatter
	// on the source has already been stripped by the reader.
	Content string
}

// Entity is a person, company, tool, or project named in the source.
// name and description are what the extract prompt instructs the LLM
// to emit; yaml tags keep the parser forgiving if the model returns
// the documented keys with different casing.
type Entity struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Concept is an idea, framework, pattern, or technique discussed in
// the source. Same shape as Entity so reconcile can iterate both in a
// single loop.
type Concept struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Claim is a factual statement or decision tagged with the entities /
// concepts it relates to. Reconcile uses Related to decide which
// pages get this claim when rendering their update prompt.
type Claim struct {
	Claim   string   `yaml:"claim"`
	Related []string `yaml:"related"`
}

// Connection captures a relationship between two named entities /
// concepts. The string form "X relates-to Y" is what gets rendered
// into prompts; the structured shape is what reconcile walks to
// decide which pages cross-reference each other.
type Connection struct {
	From         string `yaml:"from"`
	To           string `yaml:"to"`
	Relationship string `yaml:"relationship"`
}

// Extraction is the LLM's structured reading of a source. Every
// field is optional — a thin news article may produce zero concepts,
// and that's valid; reconcile handles empty lists cleanly.
type Extraction struct {
	Entities    []Entity     `yaml:"entities"`
	Concepts    []Concept    `yaml:"concepts"`
	Claims      []Claim      `yaml:"claims"`
	Connections []Connection `yaml:"connections"`
}
