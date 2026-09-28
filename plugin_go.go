package alborz

import (
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"

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
type GoPlugin struct {
	Name string
	// Files embeds public/*.html; plugins/<name>/public on disk overrides it file by file. Nil means none.
	Files fs.FS
	// CloseFunc releases resources on server close; nil means none.
	CloseFunc func() error
	// EnabledFunc reports per-request availability; nil means always.
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

// InjectFunc injects data prior to rendering a template.
type InjectFunc func(ctx *Context, data RenderData) error

// Inject registers f to run before rendering the named template; "*" matches
// any. Several for one name run in registration order.
func (p *GoPlugin) Inject(name string, f InjectFunc) {
	if p.injectFuncs == nil {
		p.injectFuncs = make(map[string]InjectFunc)
	}
	if prev, ok := p.injectFuncs[name]; ok {
		p.injectFuncs[name] = func(ctx *Context, data RenderData) error {
			if err := prev(ctx, data); err != nil {
				return err
			}
			return f(ctx, data)
		}
		return
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
