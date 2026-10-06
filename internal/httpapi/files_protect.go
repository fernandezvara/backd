package httpapi

import "github.com/fernandezvara/backd/internal/registry"

// File fields hold what backd knows about a file (its id, name, size, detected
// type and SHA-256): backd writes them, a client never does. A write that names a
// file field has it ignored, as it does the other system fields; a PUT keeps the
// stored files, since it replaces the rest of the document.

// stripFileFields drops the file fields a client sent.
func stripFileFields(c *registry.Collection, doc map[string]any) {
	for name := range c.Files {
		delete(doc, name)
	}
}

// keepFileFields copies the file fields of the stored document into dst, the new
// version of it.
func keepFileFields(c *registry.Collection, dst, current map[string]any) {
	for name := range c.Files {
		if v, ok := current[name]; ok {
			dst[name] = v
		}
	}
}
