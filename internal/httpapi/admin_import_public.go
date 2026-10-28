package httpapi

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/fernandezvara/backd/internal/imaging"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// dropFileURLs removes the `url` a package's file objects (and their versions) may carry: it is
// computed on every read from the target's public storage and never stored, so the one in the
// package says where the file was read at in the realm it came from and means nothing here.
func dropFileURLs(c *registry.Collection, doc map[string]any) {
	for _, name := range sortedKeys(c.Files) {
		for _, file := range filesOf(c.Files[name], doc) {
			delete(file, "url")
			for _, v := range mapOf(file["versions"]) {
				if state := mapOf(v); state != nil {
					delete(state, "url")
				}
			}
		}
	}
}

// checkImportedPublic checks the public files an imported document holds: the target must have a
// public storage, and the name its key ends with must be one the upload would have made, with
// the extension of a type a public file may have. The bytes are checked when they arrive.
func (a *adminAPI) checkImportedPublic(c *registry.Collection, doc map[string]any) []Detail {
	var problems []Detail
	for _, name := range sortedKeys(c.Files) {
		for _, file := range filesOf(c.Files[name], doc) {
			loc := locOf(file)
			if !loc.public {
				continue
			}
			switch clean, ok := sanitizePublicName(loc.keyName); {
			case a.reg.Realms[c.Realm].Settings.PublicStorage == nil:
				problems = append(problems, Detail{Path: name, Reason: "the package holds public files and this realm has no public_storage: configure one first"})
				return problems
			case !ok || clean != loc.keyName:
				problems = append(problems, Detail{Path: name, Reason: "file " + loc.id + " has no usable key_name for a public file"})
			case !publicExtensionMatches(loc.keyName, "image/jpeg") && !publicExtensionMatches(loc.keyName, "image/png"):
				problems = append(problems, Detail{Path: name, Reason: "file " + loc.id + ": a public file's name must end with .jpg, .jpeg or .png"})
			}
		}
	}
	return problems
}

// importPublicFile stores a public file of a package: the bytes go through the checks an upload
// does (an image of an allowed type whose extension agrees, no metadata) before they are
// published, because the package is not trusted just for having been exported. A picture that
// would have to be changed to be published is refused, since the document records the hash of
// the file as it was exported.
func (a *adminAPI) importPublicFile(w http.ResponseWriter, r *http.Request, realm string, parts []string, keyName, sum string, limit int64) bool {
	rl := a.reg.Realms[realm]
	c := rl.Databases[parts[0]].Collections[parts[1]]
	if rl.Settings.PublicStorage == nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "public", Reason: "the target realm has no public_storage"})
		return false
	}
	if clean, ok := sanitizePublicName(keyName); !ok || clean != keyName {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "key_name", Reason: "is not a usable name for a public file"})
		return false
	}
	obj, err := a.fns.docs.objects.For(r.Context(), realm, true)
	switch {
	case errors.Is(err, errStorageUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, err.Error())
		return false
	case err != nil:
		adminError(w, r, err)
		return false
	}
	body := deadlineReader{http.MaxBytesReader(w, r.Body, limit), http.NewResponseController(w)}
	br := bufio.NewReaderSize(body, 4096)
	head, peekErr := br.Peek(sniffBytes)
	if tooBig, ok := errors.AsType[*http.MaxBytesError](peekErr); ok {
		tooLarge(w, r, tooBig.Limit)
		return false
	}
	contentType := detectContentType(head, "")
	if !slicesContain(registry.PublicTypes, contentType) {
		writeError(w, r, http.StatusUnsupportedMediaType, codeUnsupportedFile, "a public file can only be "+strings.Join(registry.PublicTypes, " or ")+", and this is "+contentType+" (detected from the file's content)")
		return false
	}
	if !publicExtensionMatches(keyName, contentType) {
		writeError(w, r, http.StatusBadRequest, codeFileExtensionMismatch, "the file's content is "+contentType+" but its name is \""+keyName+"\"")
		return false
	}

	key := a.fns.docs.fileKey(c, fileLoc{id: parts[2], public: true, keyName: keyName})
	incoming, stored := sha256.New(), sha256.New()
	tee := io.TeeReader(br, incoming)
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := imaging.Strip(pw, tee, metadataFormat(contentType))
		pw.CloseWithError(err)
		if err == nil {
			_, err = io.Copy(io.Discard, tee) // what follows the picture is dropped, but counts
		}
		done <- err
	}()
	putErr := obj.PutStreamWith(r.Context(), key, io.TeeReader(pr, stored), contentType, storage.PublicOriginalHeaders)
	_ = pr.Close()
	stripErr := <-done
	remove := func() { _ = obj.Delete(r.Context(), key) }
	if err := errors.Join(putErr, stripErr); err != nil {
		remove()
		switch tooBig, isBig := errors.AsType[*http.MaxBytesError](err); {
		case isBig:
			tooLarge(w, r, tooBig.Limit)
		case errors.Is(err, imaging.ErrUnreadable):
			writeError(w, r, http.StatusUnprocessableEntity, codeInvalidImage, "the image can't be read to remove its metadata: it is damaged")
		default:
			logger(r.Context()).Error("import: store a public file", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "the storage could not take the file")
		}
		return false
	}
	switch {
	case hex.EncodeToString(incoming.Sum(nil)) != sum:
		remove()
		writeError(w, r, http.StatusBadRequest, codeChecksum, "the file arrived different from its checksum; it was not kept")
		return false
	case hex.EncodeToString(stored.Sum(nil)) != sum:
		remove()
		writeError(w, r, http.StatusUnprocessableEntity, codeInvalidImage, "the picture carries metadata that would have to be removed before it is published, which would change it from what its document records: remove it and import again")
		return false
	}
	return true
}

func slicesContain(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}
