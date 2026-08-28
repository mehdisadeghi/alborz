package alborzviewhtml

import (
	"bytes"
	"fmt"
	"html/template"
	"io/ioutil"
	"strings"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"github.com/emersion/go-message"
)

const tplSrc = `
<!-- allow-same-origin is required to resize the frame with its content -->
<!-- allow-popups is required for target="_blank" links -->
<div id="email-frame-wrap">
<iframe id="email-frame" srcdoc="{{.}}" sandbox="allow-same-origin allow-popups"></iframe>
</div>
<script src="/plugins/viewhtml/assets/script.js?v=5"></script>
<link rel="stylesheet" href="/plugins/viewhtml/assets/style.css?v=5">
`

var tpl = template.Must(template.New("view-html.html").Parse(tplSrc))

type viewer struct{}

func (viewer) ViewMessagePart(ctx *alborz.Context, msg *alborzbase.IMAPMessage, part *message.Entity) (interface{}, error) {
	allowRemoteResources := ctx.QueryParam("allow-remote-resources") == "1"

	mimeType, _, err := part.Header.ContentType()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(mimeType, "text/html") {
		return nil, alborzbase.ErrViewUnsupported
	}

	body, err := ioutil.ReadAll(part.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read part body: %v", err)
	}

	san := sanitizer{
		msg:                  msg,
		allowRemoteResources: allowRemoteResources,
	}
	body, err = san.sanitizeHTML(body)
	if err != nil {
		return nil, fmt.Errorf("failed to sanitize HTML part: %v", err)
	}

	// E-mails size images for their own layout; cap them to the frame.
	body = append([]byte(`<style>img { max-width: 100%; height: auto; }</style>`), body...)

	ctx.Set("viewhtml.hasRemoteResources", san.hasRemoteResources)

	var buf bytes.Buffer
	err = tpl.Execute(&buf, string(body))
	if err != nil {
		return nil, err
	}

	return template.HTML(buf.String()), nil
}

func init() {
	alborzbase.RegisterViewer(viewer{})
}
