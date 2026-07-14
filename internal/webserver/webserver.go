// Package webserver renders per-account web-server virtual-host configs. It is
// pure rendering (text/template from typed structs) so it is unit-testable on
// any OS via golden files; applying the output (write + validate + reload) is
// the agent's platform layer.
//
// VHostRenderer is the adapter seam: Nginx is the MVP default; a post-MVP
// Apache/OpenLiteSpeed adapter implements the same interface without touching
// callers (plan.md modular architecture).
package webserver

// VHostSpec is the desired state of one site's virtual host.
type VHostSpec struct {
	Domain    string
	Aliases   []string
	WebRoot   string // document root, e.g. /home/cyph_x/public_html
	PHPSocket string // unix socket of this account's PHP-FPM pool
	AccessLog string
	ErrorLog  string
}

// VHostRenderer renders a virtual-host config. Name identifies the engine and
// is used for logging/selection.
type VHostRenderer interface {
	Name() string
	Render(spec VHostSpec) ([]byte, error)
}
