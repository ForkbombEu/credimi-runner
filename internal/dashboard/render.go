package dashboard

import (
	"embed"
	"html/template"
	"strings"
)

//go:embed templates/*.html
var tmplFS embed.FS

// Renderer holds one template set per page (each cloned from base + partials) and
// a shared set used by the SSE poller to render live fragments.
type Renderer struct {
	pages map[string]*template.Template
	frags *template.Template
}

var funcs = template.FuncMap{
	"icon":        icon,
	"chipClass":   chipClass,
	"statusLabel": statusLabel,
	"title":       titleCase,
	"deviceIcon":  deviceIcon,
	"serviceIcon": serviceIcon,
	"deviceKind":  deviceKind,
	"modeLabel":   modeLabel,
	"isSecret":    isSecret,
	"mask":        maskSecret,
	"upper":       strings.ToUpper,
	"hasURL":      hasURL,
	"add1":        add1,
}

func NewRenderer() (*Renderer, error) {
	base := "templates/base.html"
	partials := "templates/partials.html"
	pageFiles := map[string]string{
		"overview":   "templates/overview.html",
		"devices":    "templates/devices.html",
		"workers":    "templates/workers.html",
		"network":    "templates/network.html",
		"monitoring": "templates/monitoring.html",
		"config":     "templates/config.html",
		"setup":      "templates/setup.html",
	}
	r := &Renderer{pages: map[string]*template.Template{}}
	for name, file := range pageFiles {
		t, err := template.New("base.html").Funcs(funcs).ParseFS(tmplFS, base, partials, file)
		if err != nil {
			return nil, err
		}
		r.pages[name] = t
	}
	frags, err := template.New("partials.html").Funcs(funcs).ParseFS(tmplFS, partials)
	if err != nil {
		return nil, err
	}
	r.frags = frags
	return r, nil
}

// PageData is the root context handed to every full-page render.
type PageData struct {
	Active   string
	Title    string
	Runner   *Config
	Snapshot Snapshot
	Workers  []Worker
	Pill     PillData
	Data     any // page-specific payload
}

// Page renders a full page (base layout) to the writer-equivalent string.
func (r *Renderer) Page(name string, d PageData) (string, error) {
	var b strings.Builder
	err := r.pages[name].ExecuteTemplate(&b, "base.html", d)
	return b.String(), err
}

// FragmentPage renders only the <main> block of a page (for HX-Request swaps).
func (r *Renderer) FragmentPage(name string, d PageData) (string, error) {
	var b strings.Builder
	err := r.pages[name].ExecuteTemplate(&b, "main", d)
	return b.String(), err
}

// Fragment renders a named partial (used by the SSE poller). Newlines collapse so
// the result is a single SSE data: line.
func (r *Renderer) Fragment(name string, data any) string {
	var b strings.Builder
	if err := r.frags.ExecuteTemplate(&b, name, data); err != nil {
		return "<!-- render error: " + template.HTMLEscapeString(err.Error()) + " -->"
	}
	return b.String()
}

// ── view helpers ──

func statusLabel(s Status) string {
	switch s {
	case Online:
		return "Online"
	case Degraded:
		return "Degraded"
	case Offline:
		return "Offline"
	default:
		return "Idle"
	}
}

func chipClass(s Status) string {
	return "chip " + string(s)
}

func isSecret(f Field) bool { return f.Secret }

func hasURL(s string) bool { return strings.Contains(s, "://") }

func add1(i int) int { return i + 1 }

func deviceKind(t string) string {
	switch t {
	case "android_emulator":
		return "Emulator"
	case "ios_simulator":
		return "iOS"
	case "redroid":
		return "Redroid"
	default:
		return "Android"
	}
}

func deviceIcon(t string) template.HTML {
	if strings.HasPrefix(t, "ios") {
		return icon("apple")
	}
	return icon("android")
}

func serviceIcon(id string) template.HTML {
	switch id {
	case "runner":
		return icon("server")
	case "cloudflared":
		return icon("cloud")
	case "temporal":
		return icon("workers")
	}
	return icon("server")
}

func modeLabel(m string) string {
	switch m {
	case "wifi":
		return "Wi-Fi ADB"
	case "usb":
		return "USB"
	case "emulator":
		return "KVM"
	case "simulator":
		return "simctl"
	default:
		return m
	}
}

// icon returns an inline Lucide-style SVG by name (currentColor stroke).
func icon(name string) template.HTML {
	if s, ok := icons[name]; ok {
		return template.HTML(s)
	}
	return ""
}

const svgOpen = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">`

var icons = map[string]string{
	"grid":     svgOpen + `<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>`,
	"phone":    svgOpen + `<rect x="6" y="2" width="12" height="20" rx="2.5"/><line x1="11" y1="18" x2="13" y2="18"/></svg>`,
	"workers":  svgOpen + `<path d="M12 2v4M12 18v4M4.9 4.9l2.8 2.8M16.3 16.3l2.8 2.8M2 12h4M18 12h4M4.9 19.1l2.8-2.8M16.3 7.7l2.8-2.8"/><circle cx="12" cy="12" r="3.2"/></svg>`,
	"network":  svgOpen + `<circle cx="12" cy="5" r="2.5"/><circle cx="5" cy="19" r="2.5"/><circle cx="19" cy="19" r="2.5"/><path d="M12 7.5v4M12 11.5L6 16.7M12 11.5l6 5.2"/></svg>`,
	"key":      svgOpen + `<circle cx="7.5" cy="15.5" r="4.5"/><path d="M10.7 12.3L20 3M16 7l3 3M14 9l2 2"/></svg>`,
	"server":   svgOpen + `<rect x="3" y="4" width="18" height="7" rx="1.6"/><rect x="3" y="13" width="18" height="7" rx="1.6"/><line x1="7" y1="7.5" x2="7.01" y2="7.5"/><line x1="7" y1="16.5" x2="7.01" y2="16.5"/></svg>`,
	"shield":   svgOpen + `<path d="M12 2.5l8 3.2v5.5c0 4.6-3.2 8.9-8 10.3-4.8-1.4-8-5.7-8-10.3V5.7l8-3.2z"/></svg>`,
	"cloud":    svgOpen + `<path d="M17.5 19a4.5 4.5 0 0 0 .3-9 6 6 0 0 0-11.5-1.5A4 4 0 0 0 6.5 19z"/></svg>`,
	"activity": svgOpen + `<path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>`,
	"globe":    svgOpen + `<circle cx="12" cy="12" r="9.5"/><path d="M2.5 12h19M12 2.5c2.6 2.6 4 6 4 9.5s-1.4 6.9-4 9.5c-2.6-2.6-4-6-4-9.5s1.4-6.9 4-9.5z"/></svg>`,
	"check":    `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>`,
	"x":        `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>`,
	"plus":     `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>`,
	"refresh":  `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 2v6h-6M3 12a9 9 0 0 1 15-6.7L21 8M3 22v-6h6M21 12a9 9 0 0 1-15 6.7L3 16"/></svg>`,
	"trash":    `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/></svg>`,
	"wifi":     svgOpen + `<path d="M2 8.5a15 15 0 0 1 20 0M5 12a10 10 0 0 1 14 0M8.5 15.5a5 5 0 0 1 7 0"/><circle cx="12" cy="19" r="1" fill="currentColor"/></svg>`,
	"usb":      svgOpen + `<circle cx="12" cy="20" r="1.4"/><path d="M12 18.6V5M12 5l-2.4 3h4.8L12 5zM12 13l-3.2-3.2v-2M12 13l3.2-3.2"/><rect x="14" y="6" width="2.6" height="2.6" rx="0.4"/></svg>`,
	"info":     `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9.5"/><line x1="12" y1="11" x2="12" y2="16"/><circle cx="12" cy="8" r="0.6" fill="currentColor"/></svg>`,
	"warn":     `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M10.3 3.3 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.3a2 2 0 0 0-3.4 0z"/><line x1="12" y1="9" x2="12" y2="13"/><circle cx="12" cy="17" r="0.6" fill="currentColor"/></svg>`,
	"eye":      `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1.5 12S5 5 12 5s10.5 7 10.5 7-3.5 7-10.5 7S1.5 12 1.5 12z"/><circle cx="12" cy="12" r="3"/></svg>`,
	"copy":     `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg>`,
	"gear":     `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.04.04a2 2 0 0 1-2.83 2.83l-.04-.04A1.7 1.7 0 0 0 15 19.4a1.7 1.7 0 0 0-1 .55V20a2 2 0 0 1-4 0v-.05a1.7 1.7 0 0 0-1-.55 1.7 1.7 0 0 0-1.88.34l-.04.04a2 2 0 0 1-2.83-2.83l.04-.04A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-.55-1H4a2 2 0 0 1 0-4h.05a1.7 1.7 0 0 0 .55-1 1.7 1.7 0 0 0-.34-1.88l-.04-.04a2 2 0 0 1 2.83-2.83l.04.04A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-.55V4a2 2 0 0 1 4 0v.05a1.7 1.7 0 0 0 1 .55 1.7 1.7 0 0 0 1.88-.34l.04-.04a2 2 0 0 1 2.83 2.83l-.04.04A1.7 1.7 0 0 0 19.4 9c.2.36.39.7.55 1H20a2 2 0 0 1 0 4h-.05a1.7 1.7 0 0 0-.55 1z"/></svg>`,
	"android":  `<svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor"><path d="M6 9v7a1.5 1.5 0 0 0 1.5 1.5H8V20a1.2 1.2 0 0 0 2.4 0v-2.5h3.2V20a1.2 1.2 0 0 0 2.4 0v-2.5h.5A1.5 1.5 0 0 0 18 16V9H6zM4.2 9A1.2 1.2 0 0 0 3 10.2v4.6a1.2 1.2 0 0 0 2.4 0v-4.6A1.2 1.2 0 0 0 4.2 9zm15.6 0a1.2 1.2 0 0 0-1.2 1.2v4.6a1.2 1.2 0 0 0 2.4 0v-4.6A1.2 1.2 0 0 0 19.8 9zM15.5 4.3l1-1.5a.3.3 0 0 0-.5-.34l-1.1 1.6A5.7 5.7 0 0 0 12 3.5c-1.04 0-2 .2-2.9.56L8 2.46a.3.3 0 1 0-.5.34l1 1.5A5.3 5.3 0 0 0 6 8.3h12a5.3 5.3 0 0 0-2.5-4z"/></svg>`,
	"apple":    `<svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor"><path d="M16 2c-1.1.07-2.4.77-3.15 1.7-.68.83-1.27 2.06-1.05 3.26 1.2.04 2.45-.68 3.18-1.6.69-.86 1.21-2.07 1.02-3.36zM18.6 12.4c-.03-2.5 2.04-3.7 2.13-3.76-1.16-1.7-2.97-1.93-3.61-1.96-1.54-.15-3 .9-3.78.9-.78 0-1.98-.88-3.25-.86-1.67.03-3.21.97-4.07 2.46-1.73 3-.44 7.44 1.25 9.88.82 1.19 1.8 2.53 3.08 2.48 1.24-.05 1.71-.8 3.2-.8 1.5 0 1.92.8 3.23.78 1.33-.03 2.18-1.21 3-2.41.94-1.38 1.33-2.72 1.35-2.79-.03-.01-2.6-1-2.63-3.96z"/></svg>`,
}
