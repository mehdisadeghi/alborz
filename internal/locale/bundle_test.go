package locale_test

import (
	"testing"
	"testing/fstest"

	"git.mehdix.org/alborz/internal/locale"
)

func TestRegisterFSMergesWithCore(t *testing.T) {
	files := fstest.MapFS{}
	for _, code := range locale.Languages {
		files[code+".toml"] = &fstest.MapFile{Data: []byte(`"addon.probe" = "` + code + `"`)}
	}
	if err := locale.RegisterFS(files); err != nil {
		t.Fatal(err)
	}
	for _, code := range locale.Languages {
		if got := locale.T(code, "addon.probe"); got != code {
			t.Errorf("%s add-on translation = %q", code, got)
		}
		if got := locale.T(code, "common.save"); got == "common.save" {
			t.Errorf("%s core translation lost", code)
		}
	}
}
