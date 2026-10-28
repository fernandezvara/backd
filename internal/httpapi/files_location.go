package httpapi

import (
	"mime"
	"net/http"
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

// publicUploadsAvailable answers 501 to an upload to a public field: the storage is chosen per
// file now, but the upload of a public file (its name, its checks, its headers) comes with the
// next step of the public files work. Remove this with it.
func publicUploadsAvailable(w http.ResponseWriter, r *http.Request, f *registry.FileField) bool {
	if !f.Public {
		return true
	}
	writeError(w, r, http.StatusNotImplemented, "not_implemented", "uploads to public fields are not available yet")
	return false
}
