package alborzviewtext

import (
	"git.mehdix.org/alborz"
)

func init() {
	p := alborz.GoPlugin{Name: "viewtext"}
	alborz.RegisterPluginLoader(p.Loader())
}
