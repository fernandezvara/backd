package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
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
	ch16 := ch13 + "\n" + blockAfter(t, "16-second-factor", "Add to `config/shelf/realm.yaml`:") +
		"\n" + blockAfter(t, "16-second-factor", "Add this to `config/shelf/realm.yaml` too:")
	ch17 := ch16 + "\n" + blockAfter(t, "17-consents", "Add to `config/shelf/realm.yaml`:")
	ch20 := ch17 + "\n" + blockAfter(t, "20-teams", "Add to `config/shelf/realm.yaml`:")
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
	t.Run("chapter 20", func(t *testing.T) {
		loadChapter(t, func(d string) {
			src := filepath.Join("..", "..", "examples", "config", "shelf")
			if err := os.CopyFS(d, os.DirFS(src)); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(d, "realm.yaml"), ch20)
		})
	})
	// Chapter 26 replaces the consents (adding the privacy notice) and adds the export settings.
	ch26 := func() string {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(ch20), &m); err != nil {
			t.Fatal(err)
		}
		for _, snippet := range []string{
			blockAfter(t, "26-your-data", "Replace the `consents` of `config/shelf/realm.yaml` with:"),
			blockAfter(t, "26-your-data", "Add this to `config/shelf/realm.yaml` too:"),
		} {
			var add map[string]any
			if err := yaml.Unmarshal([]byte(snippet), &add); err != nil {
				t.Fatal(err)
			}
			for k, v := range add {
				m[k] = v
			}
		}
		out, err := yaml.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}()
	t.Run("chapter 26 is the finished realm", func(t *testing.T) {
		loadChapter(t, func(d string) {
			src := filepath.Join("..", "..", "examples", "config", "shelf")
			if err := os.CopyFS(d, os.DirFS(src)); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(d, "realm.yaml"), ch26)
		})
		final, err := os.ReadFile(filepath.Join("..", "..", "examples", "config", "shelf", "realm.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var a, b map[string]any
		if err := yaml.Unmarshal(final, &a); err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal([]byte(ch26), &b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("the chapters' realm.yaml snippets do not add up to examples/config/shelf/realm.yaml:\nchapters: %v\nrepository: %v", b, a)
		}
	})
}

// Chapter 21 prints the reference it adds to the assets: it has to be what the finished
// realm declares, and the realm has to load with it (profiles at the user's id included).
func TestTutorialAuthorsReference(t *testing.T) {
	printed := blockAfter(t, "21-authors", "Add this at the end of `config/shelf/main/assets/collection.yaml`")
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "config", "shelf", "main", "assets", "collection.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var want, got struct {
		References map[string]any `yaml:"references"`
	}
	if err := yaml.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(printed), &got); err != nil {
		t.Fatal(err)
	}
	if len(want.References) == 0 || !reflect.DeepEqual(want.References, got.References) {
		t.Fatalf("chapter 21 prints %v, the repository has %v", got.References, want.References)
	}
	reg, err := Load(filepath.Join("..", "..", "examples", "config"))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := reg.Collection("shelf", "main", "assets")
	if !ok || c.References["author"] == nil || c.References["author"].Key != "id" {
		t.Fatalf("assets references = %+v", c.References)
	}
}

// Chapter 22 prints what it adds: the text index entry, the aggregate section and the index the
// warning asks for. They have to be in the finished realm, which has to load with them.
func TestTutorialSearchAndDashboard(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "config", "shelf", "main")
	read := func(p string) []byte {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// The aggregate section printed in the chapter is the one of pins/collection.yaml.
	printed := blockAfter(t, "22-search-dashboard", "add this at the end of `config/shelf/main/pins/collection.yaml`")
	var want, got struct {
		Aggregate map[string]any `yaml:"aggregate"`
	}
	if err := yaml.Unmarshal(read("pins/collection.yaml"), &want); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(printed), &got); err != nil {
		t.Fatal(err)
	}
	if len(want.Aggregate) == 0 || !reflect.DeepEqual(want.Aggregate, got.Aggregate) {
		t.Fatalf("chapter 22 prints %v, the repository has %v", got.Aggregate, want.Aggregate)
	}
	reg, err := Load(filepath.Join("..", "..", "examples", "config"))
	if err != nil {
		t.Fatal(err)
	}
	pins, _ := reg.Collection("shelf", "main", "pins")
	assets, _ := reg.Collection("shelf", "main", "assets")
	if pins.Aggregate == nil || len(pins.AggregateIndexWarnings()) != 0 {
		t.Errorf("pins: aggregate %+v, warnings %v: the index of the chapter should silence the warning", pins.Aggregate, pins.AggregateIndexWarnings())
	}
	if assets.Text == nil || assets.Text.Fields[0].Weight == 0 {
		t.Errorf("assets text index = %+v", assets.Text)
	}
	// Without the index the chapter shows, the warning is the one the chapter prints.
	without := *pins
	without.Indexes = slices.DeleteFunc(slices.Clone(pins.Indexes), func(ix Index) bool { return ix.Keys[0].Field == "author" })
	if w := without.AggregateIndexWarnings(); len(w) != 1 || !strings.Contains(w[0], `"author" has no index that starts with it`) {
		t.Errorf("warnings without the index = %v", w)
	}
}
