package web

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
)

func renderData(ctx *web.Context, status int, alert string) error {
	return ctx.Render(status, "settings/data", web.NewSettingsPageData(ctx, ctx.T("settings.data")).WithRefused(alert))
}

func handleData(ctx *web.Context) error { return renderData(ctx, http.StatusOK, "") }

func handleExport(ctx *web.Context) error {
	name := alborz.AppName + "-" + ctx.Login.Username() + "-" + time.Now().Format(time.DateOnly) + ".tar.gz"
	res := ctx.Response()
	res.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	res.Header().Set("Content-Type", "application/gzip")
	res.WriteHeader(http.StatusOK)
	// Too late for an answer once the archive has begun: the download stops short and the log says why.
	if err := ctx.Server.Export(ctx.Request().Context(), ctx.Login, res); err != nil {
		ctx.Logger().Printf("export of %q stopped: %v", ctx.Login.Username(), err)
	}
	return nil
}

// The archive is read as it arrives, never spooled to disk.
func handleImport(ctx *web.Context) error {
	form, err := ctx.Request().MultipartReader()
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}
	var file *multipart.Part
	for file == nil {
		part, err := form.NextPart()
		if err == io.EOF {
			return renderData(ctx, http.StatusUnprocessableEntity, ctx.T("form.fileneeded"))
		}
		if err != nil {
			return importRefusal(ctx, err)
		}
		if part.FormName() == "file" {
			file = part
		}
	}
	notes, err := ctx.Server.Import(ctx.Request().Context(), ctx.Login, file)
	if err != nil {
		return importRefusal(ctx, err)
	}
	said := []string{ctx.T("notice.dataimported")}
	names := map[string][]string{}
	var keys []string
	for _, n := range notes {
		if names[n.Key] == nil {
			keys = append(keys, n.Key)
		}
		names[n.Key] = append(names[n.Key], n.Name)
	}
	for _, k := range keys {
		said = append(said, ctx.Tf(k, len(names[k]), strings.Join(names[k], ", ")))
	}
	return ctx.NoticeRedirect(strings.Join(said, " "), ctx.URL("settings.data"))
}

func importRefusal(ctx *web.Context, err error) error {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return renderData(ctx, http.StatusRequestEntityTooLarge, ctx.Tf("data.toolarge", alborz.ArchiveMaxSize>>20))
	case errors.Is(err, alborz.ErrNotArchive):
		ctx.Logger().Printf("import for %q refused: %v", ctx.Login.Username(), err)
		return renderData(ctx, http.StatusUnprocessableEntity, ctx.T("data.notarchive"))
	case errors.Is(err, errors.ErrUnsupported), errors.Is(err, alborz.ErrLocked):
		return renderData(ctx, http.StatusUnprocessableEntity, ctx.SaveRefusal(err))
	}
	return err
}
