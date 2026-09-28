package alborz

import (
	"html/template"

	"github.com/labstack/echo/v4"
)

// PluginDir is the path to the plugins directory.
const PluginDir = "plugins"

// Plugin extends alborz with additional functionality.
type Plugin interface {
	// Name returns the plugin name.
	Name() string
	// LoadTemplate populates t with the plugin's functions and templates.
	LoadTemplate(t *template.Template) error
	// SetRoutes populates group with the plugin's routes.
	SetRoutes(group *echo.Group)
	// Close is called when the plugin is unloaded.
	Close() error
}

// PluginLoaderFunc loads plugins for the provided server.
type PluginLoaderFunc func(*Server) ([]Plugin, error)

var pluginLoaders []PluginLoaderFunc

// RegisterPluginLoader registers a plugin loader, called on server start-up.
func RegisterPluginLoader(f PluginLoaderFunc) {
	pluginLoaders = append(pluginLoaders, f)
}
