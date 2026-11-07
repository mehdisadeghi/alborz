package web

import (
	"testing"
	"testing/fstest"
)

func TestTemplateLoaderIgnoresColocatedStyles(t *testing.T) {
	files := fstest.MapFS{
		"layout.html":          {Data: []byte(`{{define "layout"}}{{template "main" .}}{{end}}{{define "main"}}{{end}}`)},
		"partials/shared.html": {Data: []byte(`{{define "shared"}}Shared{{end}}`)},
		"partials/shared.css":  {Data: []byte(`{{ invalid template syntax`)},
		"pages/example.html":   {Data: []byte(`{{define "main"}}Example{{end}}`)},
		"pages/example.css":    {Data: []byte(`{{ invalid template syntax`)},
	}
	addon := fstest.MapFS{
		"partials/extra.html": {Data: []byte(`{{define "extra"}}Extra{{end}}`)},
		"partials/extra.css":  {Data: []byte(`{{ invalid template syntax`)},
		"pages/extra.html":    {Data: []byte(`{{define "main"}}Extra page{{end}}`)},
		"pages/extra.css":     {Data: []byte(`{{ invalid template syntax`)},
	}
	renderer, err := loadTemplates(files, nil, []AddonBundle{{Name: "example", Files: addon}})
	if err != nil {
		t.Fatal(err)
	}
	if len(renderer.pages) != 2 || renderer.pages["example"] == nil || renderer.pages["extra"] == nil {
		t.Fatalf("expected only the core and add-on HTML pages, got %v", renderer.pages)
	}
	if renderer.partials.Lookup("shared") == nil || renderer.partials.Lookup("extra") == nil {
		t.Fatal("core or add-on HTML partial was skipped")
	}
}
