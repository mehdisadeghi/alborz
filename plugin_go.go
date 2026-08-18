package alborz

import (
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

type goPlugin struct {
	p *GoPlugin
}

func (p *goPlugin) Name() string {
	return p.p.Name
}

func (p *goPlugin) LoadTemplate(t *template.Template) error {
	t.Funcs(p.p.templateFuncs)

	if p.p.Files != nil {
		names, err := fs.Glob(p.p.Files, "public/*.html")
		if err != nil {
			return err
		}
		if len(names) > 0 {
			if _, err := t.ParseFS(p.p.Files, "public/*.html"); err != nil {
				return err
			}
		}
	}

	paths, err := filepath.Glob(PluginDir + "/" + p.p.Name + "/public/*.html")
	if err != nil {
		return err
	}
	if len(paths) > 0 {
		if _, err := t.ParseFiles(paths...); err != nil {
			return err
		}
	}

	return nil
}

func (p *goPlugin) SetRoutes(group *echo.Group) {
	for _, r := range p.p.routes {
		h := r.Handler
		group.Add(r.Method, r.Path, func(ectx echo.Context) error {
			return h(ectx.(*Context))
		})
	}

	// Assets are served from the embedded files, with the plugin directory
	// on disk taking precedence file by file, like templates.
	group.GET("/plugins/"+p.p.Name+"/assets/*", func(ectx echo.Context) error {
		name := strings.TrimPrefix(path.Clean("/"+ectx.Param("*")), "/")
		if name == "" {
			return echo.ErrNotFound
		}
		diskPath := filepath.Join(PluginDir, p.p.Name, "public", "assets", name)
		if _, err := os.Stat(diskPath); err == nil {
			return ectx.File(diskPath)
		}
		if p.p.Files == nil {
			return echo.ErrNotFound
		}
		http.ServeFileFS(ectx.Response(), ectx.Request(), p.p.Files, path.Join("public/assets", name))
		return nil
	})
}

func (p *goPlugin) Inject(ctx *Context, name string, data RenderData) error {
	if f, ok := p.p.injectFuncs["*"]; ok {
		if err := f(ctx, data); err != nil {
			return err
		}
	}
	if f, ok := p.p.injectFuncs[name]; ok {
		return f(ctx, data)
	}
	return nil
}

func (p *goPlugin) Enabled(ctx *Context) bool {
	if p.p.EnabledFunc == nil {
		return true
	}
	return p.p.EnabledFunc(ctx)
}

func (p *goPlugin) Close() error {
	if p.p.CloseFunc == nil {
		return nil
	}
	return p.p.CloseFunc()
}

type goPluginRoute struct {
	Method  string
	Path    string
	Handler HandlerFunc
}

// GoPlugin is a helper to create Go plugins.
//
// Use this struct to define your plugin, then call RegisterPluginLoader:
//
//	p := GoPlugin{Name: "my-plugin"}
//	// Define routes, template functions, etc
//	alborz.RegisterPluginLoader(p.Loader())
type GoPlugin struct {
	Name string
	// Files is the plugin's embedded public directory: templates in
	// public/*.html and assets in public/assets. A file with the same
	// name in plugins/<name>/public on disk overrides its embedded
	// counterpart. Nil means the plugin ships no templates or assets.
	Files fs.FS
	// CloseFunc releases plugin resources on server close or reload;
	// nil means nothing to release.
	CloseFunc func() error
	// EnabledFunc reports per-request availability; nil means always
	// enabled.
	EnabledFunc func(*Context) bool

	routes []goPluginRoute

	templateFuncs template.FuncMap
	injectFuncs   map[string]InjectFunc
}

// HandlerFunc is a function serving HTTP requests.
type HandlerFunc func(*Context) error

// AddRoute registers a new HTTP route.
func (p *GoPlugin) AddRoute(method, path string, handler HandlerFunc) {
	p.routes = append(p.routes, goPluginRoute{method, path, handler})
}

func (p *GoPlugin) DELETE(path string, handler HandlerFunc) {
	p.AddRoute(http.MethodDelete, path, handler)
}

func (p *GoPlugin) GET(path string, handler HandlerFunc) {
	p.AddRoute(http.MethodGet, path, handler)
}

func (p *GoPlugin) POST(path string, handler HandlerFunc) {
	p.AddRoute(http.MethodPost, path, handler)
}

func (p *GoPlugin) PUT(path string, handler HandlerFunc) {
	p.AddRoute(http.MethodPut, path, handler)
}

// TemplateFuncs registers new template functions.
func (p *GoPlugin) TemplateFuncs(funcs template.FuncMap) {
	if p.templateFuncs == nil {
		p.templateFuncs = make(template.FuncMap, len(funcs))
	}

	for k, f := range funcs {
		p.templateFuncs[k] = f
	}
}

// InjectFunc is a function that injects data prior to rendering a template.
type InjectFunc func(ctx *Context, data RenderData) error

// Inject registers a function to execute prior to rendering a template. The
// special name "*" matches any template.
func (p *GoPlugin) Inject(name string, f InjectFunc) {
	if p.injectFuncs == nil {
		p.injectFuncs = make(map[string]InjectFunc)
	}
	p.injectFuncs[name] = f
}

// Plugin returns an object implementing Plugin.
func (p *GoPlugin) Plugin() Plugin {
	return &goPlugin{p}
}

// Loader returns a loader function for this plugin.
func (p *GoPlugin) Loader() PluginLoaderFunc {
	return func(*Server) ([]Plugin, error) {
		return []Plugin{p.Plugin()}, nil
	}
}
