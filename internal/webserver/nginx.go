package webserver

import (
	"bytes"
	"fmt"
	"text/template"
)

// Nginx is the MVP-default VHostRenderer.
type Nginx struct{}

func (Nginx) Name() string { return "nginx" }

var nginxTmpl = template.Must(template.New("nginx-vhost").Parse(`# Managed by CypherPanel — do not edit by hand.
server {
    listen 80;
    listen [::]:80;
    server_name {{ .Domain }}{{ range .Aliases }} {{ . }}{{ end }};

    root {{ .WebRoot }};
    index index.php index.html index.htm;

    access_log {{ .AccessLog }};
    error_log {{ .ErrorLog }};

    location / {
        try_files $uri $uri/ /index.php?$query_string;
    }

    location ~ \.php$ {
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_pass unix:{{ .PHPSocket }};
        fastcgi_index index.php;
    }

    # Deny access to hidden files except ACME challenges.
    location ~ /\.(?!well-known).* {
        deny all;
    }
}
`))

func (Nginx) Render(spec VHostSpec) ([]byte, error) {
	if spec.Domain == "" || spec.WebRoot == "" || spec.PHPSocket == "" {
		return nil, fmt.Errorf("webserver: vhost spec missing domain, web root, or php socket")
	}
	var buf bytes.Buffer
	if err := nginxTmpl.Execute(&buf, spec); err != nil {
		return nil, fmt.Errorf("webserver: rendering nginx vhost for %s: %w", spec.Domain, err)
	}
	return buf.Bytes(), nil
}
