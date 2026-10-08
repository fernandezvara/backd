package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The tutorial (docs/content/docs/tutorial) shows realm.yaml and collection.yaml
// inline, chapter by chapter, and the files of examples/config/shelf are the
// finished state. These tests load what a reader would have after chapters 1,
// 2, 3 and 4, and check that the chapters add up to the finished realm.yaml —
// so a chapter can't print a file that doesn't load, or drift from the
// repository.

var yamlBlock = regexp.MustCompile("(?s)```yaml\n(.*?)```")

// blockAfter returns the first ```yaml block that follows marker in a chapter.
func blockAfter(t *testing.T, chapter, marker string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "content", "docs", "tutorial", chapter, "index.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	i := strings.Index(text, marker)
	if i < 0 {
		t.Fatalf("%s: %q not found", chapter, marker)
	}
	m := yamlBlock.FindStringSubmatch(text[i:])
	if m == nil {
		t.Fatalf("%s: no yaml block after %q", chapter, marker)
	}
	return m[1]
}

func copyShelf(t *testing.T, dst string, files ...string) {
	t.Helper()
	src := filepath.Join("..", "..", "examples", "config", "shelf")
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dst, f), string(b))
	}
}

// beforeFiles copies shelf files like copyShelf, but keeps a collection.yaml only up
// to chapter 13's `files:` section: what a reader has before they add it.
func beforeFiles(t *testing.T, dst string, files ...string) {
	t.Helper()
	src := filepath.Join("..", "..", "examples", "config", "shelf")
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		if i := strings.Index(text, "\n# Chapter 13:"); i >= 0 {
			text = text[:i+1]
		}
		writeFile(t, filepath.Join(dst, f), text)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadChapter(t *testing.T, tree func(dir string)) {
	t.Helper()
	root := t.TempDir()
	tree(filepath.Join(root, "shelf"))
	if _, err := Load(root); err != nil {
		t.Fatalf("the reader's config does not load: %v", err)
	}
}

func TestTutorialChaptersLoad(t *testing.T) {
	ch1 := blockAfter(t, "01-empty-shelf", "Create `config/shelf/realm.yaml`:")
	ch2 := blockAfter(t, "02-members", "Replace `config/shelf/realm.yaml`:")
	ch4 := ch2
	if !strings.Contains(blockAfter(t, "04-invitations", "## Close the door"), "signup: invite") {
		t.Fatal("chapter 4 no longer sets signup: invite")
	}
	ch4 = strings.Replace(ch2, "signup: open", "signup: invite", 1)
	ch10 := ch4 + "\n" + blockAfter(t, "10-email", "Add to `config/shelf/realm.yaml`")
	ch13 := ch10 + "\n" + blockAfter(t, "13-files", "Add to `config/shelf/realm.yaml`")
	collections := []string{
		"main/assets/schema.json", "main/assets/indexes.json", "main/shares/schema.json", "main/shares/indexes.json",
	}

	t.Run("chapter 1", func(t *testing.T) {
		loadChapter(t, func(d string) {
			writeFile(t, filepath.Join(d, "realm.yaml"), ch1)
			copyShelf(t, d, collections...)
		})
	})
	t.Run("chapter 2", func(t *testing.T) {
		loadChapter(t, func(d string) {
			writeFile(t, filepath.Join(d, "realm.yaml"), ch2)
			beforeFiles(t, d, collections...)
			// Chapter 2 writes the erase policy of both collections.
			policy := blockAfter(t, "02-members", "Create `config/shelf/main/assets/collection.yaml`")
			writeFile(t, filepath.Join(d, "main/assets/collection.yaml"), policy)
			writeFile(t, filepath.Join(d, "main/shares/collection.yaml"), policy)
		})
	})
	t.Run("chapters 3 and 4", func(t *testing.T) {
		loadChapter(t, func(d string) {
			writeFile(t, filepath.Join(d, "realm.yaml"), ch4)
			beforeFiles(t, d, collections...)
			// Chapter 3 shows both collection.yaml files whole: the rules and chapter 2's policy.
			writeFile(t, filepath.Join(d, "main/assets/collection.yaml"), blockAfter(t, "03-rules", "Replace `config/shelf/main/assets/collection.yaml`"))
			writeFile(t, filepath.Join(d, "main/shares/collection.yaml"), blockAfter(t, "03-rules", "And `config/shelf/main/shares/collection.yaml`"))
		})
	})
	t.Run("chapter 13 is the finished realm", func(t *testing.T) {
		loadChapter(t, func(d string) {
			src := filepath.Join("..", "..", "examples", "config", "shelf")
			if err := os.CopyFS(d, os.DirFS(src)); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(d, "realm.yaml"), ch13)
		})
		final, err := os.ReadFile(filepath.Join("..", "..", "examples", "config", "shelf", "realm.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var a, b map[string]any
		if err := yaml.Unmarshal(final, &a); err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal([]byte(ch13), &b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("the chapters' realm.yaml snippets do not add up to examples/config/shelf/realm.yaml:\nchapters: %v\nrepository: %v", b, a)
		}
	})
}
