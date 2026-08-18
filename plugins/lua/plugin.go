package alborzlua

import (
	"git.mehdix.org/alborz"
)

func init() {
	alborz.RegisterPluginLoader(loadAllLuaPlugins)
}
