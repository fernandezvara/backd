package httpapi

import (
	"maps"
	"mime"
	"path"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// Where a file's objects are. A file is stored in the realm's private storage or, for a public
// field, in its public storage, and that is decided when it is uploaded: the file's details say
// which (public: true) and the name its key carries (key_name), because changing a field's
// setting later moves nothing. Everything that reaches a file's object asks for it through a
// fileLoc, so the choice between the two storages is made in one place (realmObjects.For and the
// two functions below) and nowhere else compares public.
type fileLoc struct {
	id      string
	public  bool
	keyName string // public files only: the sanitized name the object's key ends with
}

// locOf reads where a file is from its details in the document.
func locOf(meta map[string]any) fileLoc {
	id, _ := meta["id"].(string)
	public, _ := meta["public"].(bool)
	name, _ := meta["key_name"].(string)
	return fileLoc{id: id, public: public, keyName: name}
}

// storageFor returns the settings of the storage the file is in, or nil when the realm has none.
func (d *documents) storageFor(c *registry.Collection, public bool) *registry.StorageSettings {
	return d.reg.Realms[c.Realm].Settings.StorageFor(public)
}

// fileKey is the key of a file's original object, which needs no connection to the storage.
func (d *documents) fileKey(c *registry.Collection, ref fileLoc) string {
	prefix := ""
	if st := d.storageFor(c, ref.public); st != nil {
		prefix = st.Prefix
	}
	if ref.public {
		return storage.PublicObjectKey(prefix, c.Realm, c.Database, c.Name, ref.id, ref.keyName)
	}
	return storage.ObjectKey(prefix, c.Realm, c.Database, c.Name, ref.id)
}

// versionKey is the key of a made version of a file. state is the version's details in the file
// (its type gives a public version the extension of what was made).
func (d *documents) versionKey(c *registry.Collection, ref fileLoc, version string, state map[string]any) string {
	if !ref.public {
		return d.fileKey(c, ref) + "/" + version
	}
	prefix := ""
	if st := d.storageFor(c, true); st != nil {
		prefix = st.Prefix
	}
	ct, _ := state["type"].(string)
	return storage.PublicVersionKey(prefix, c.Realm, c.Database, c.Name, ref.id, version, versionKeyName(ref.keyName, ct))
}

// versionKeyName is a public file's name with the extension of the type a version was made in.
func versionKeyName(keyName, contentType string) string {
	ext := ""
	switch ct, _, _ := mime.ParseMediaType(contentType); ct {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	}
	if ext == "" {
		return keyName
	}
	stem := strings.TrimSuffix(keyName, path.Ext(keyName))
	return stem + ext
}

// erasedLoc is where an erased file's objects were.
func erasedLoc(f storage.ErasedFile) fileLoc {
	return fileLoc{id: f.ID, public: f.Public, keyName: f.KeyName}
}

// publicURL is the address a public file's object is read at: the public storage's url and the key.
func (d *documents) publicURL(c *registry.Collection, key string) string {
	return d.storageFor(c, true).URL + "/" + storage.EscapeKey(key)
}

// renderIn is render for a document of collection c: it also gives every public file the
// address it is read at (`url`), and the same to each of its made versions. The address is
// computed here, on every read, from the key the file records and the public storage's url
// (never stored), so moving the CDN touches no document.
func (d *documents) renderIn(c *registry.Collection, doc map[string]any) map[string]any {
	return render(d.withPublicURLs(c, doc))
}

// withPublicURLs returns doc with `url` in its public files (and their ready versions); doc
// itself when it has none. Which files are public is their own record, not the field's setting.
func (d *documents) withPublicURLs(c *registry.Collection, doc map[string]any) map[string]any {
	if len(c.Files) == 0 || d.storageFor(c, true) == nil {
		return doc
	}
	var out map[string]any
	for name, f := range c.Files {
		files := filesOf(f, doc)
		changed := false
		withURL := make([]map[string]any, len(files))
		for i, file := range files {
			withURL[i] = file
			loc := locOf(file)
			if !loc.public {
				continue
			}
			cp := maps.Clone(file)
			cp["url"] = d.publicURL(c, d.fileKey(c, loc))
			if versions := mapOf(file["versions"]); versions != nil {
				vs := make(map[string]any, len(versions))
				for vname, v := range versions {
					state := mapOf(v)
					if state != nil && state["status"] == versionReady {
						state = maps.Clone(state)
						state["url"] = d.publicURL(c, d.versionKey(c, loc, vname, state))
					}
					vs[vname] = state
				}
				cp["versions"] = vs
			}
			withURL[i], changed = cp, true
		}
		if !changed {
			continue
		}
		if out == nil {
			out = maps.Clone(doc)
		}
		if f.Multiple {
			list := make([]any, len(withURL))
			for i, e := range withURL {
				list[i] = e
			}
			out[name] = list
		} else {
			out[name] = withURL[0]
		}
	}
	if out == nil {
		return doc
	}
	return out
}

// publicTag is the part of a read's validator that depends on where public files are read from:
// changing the public storage's url changes it, so clients refetch and get the new addresses
// instead of keeping dead ones under a 304. It is empty (and changes nothing) for documents
// without public files.
func (d *documents) publicTag(c *registry.Collection, docs ...map[string]any) []string {
	st := d.storageFor(c, true)
	if st == nil {
		return nil
	}
	for _, doc := range docs {
		for _, f := range c.Files {
			for _, file := range filesOf(f, doc) {
				if locOf(file).public {
					return []string{"public-url:" + st.URL}
				}
			}
		}
	}
	return nil
}
