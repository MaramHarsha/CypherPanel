package proxy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// TraefikWriter implements the docker.Router interface by writing Traefik file
// provider YAML configurations.
type TraefikWriter struct {
	appsDir string
}

// NewTraefikWriter creates a router that manages Traefik fragments in appsDir.
func NewTraefikWriter(appsDir string) *TraefikWriter {
	return &TraefikWriter{
		appsDir: appsDir,
	}
}

// SetRoute writes the Traefik fragment for an app atomically.
func (t *TraefikWriter) SetRoute(ctx context.Context, appID string, route *agentv1.RouteSpec, upstream string) error {
	if route == nil {
		return fmt.Errorf("route spec is nil")
	}
	if strings.Contains(appID, "..") || strings.Contains(appID, "/") || strings.Contains(appID, "\\") {
		return fmt.Errorf("invalid appID")
	}
	if strings.Contains(route.Domain, "`") {
		return fmt.Errorf("invalid domain")
	}
	if strings.Contains(route.PathPrefix, "`") || strings.Contains(route.PathPrefix, "&&") || strings.Contains(route.PathPrefix, "||") {
		return fmt.Errorf("invalid path prefix")
	}

	if err := os.MkdirAll(t.appsDir, 0755); err != nil {
		return fmt.Errorf("creating traefik apps dir: %w", err)
	}

	type Service struct {
		LoadBalancer struct {
			Servers []struct {
				URL string `yaml:"url"`
			} `yaml:"servers"`
		} `yaml:"loadBalancer"`
	}
	type Router struct {
		Rule    string `yaml:"rule"`
		Service string `yaml:"service"`
		TLS     *struct {
			CertResolver string `yaml:"certResolver,omitempty"`
		} `yaml:"tls,omitempty"`
	}

	doc := struct {
		HTTP struct {
			Routers  map[string]Router  `yaml:"routers"`
			Services map[string]Service `yaml:"services"`
		} `yaml:"http"`
	}{}

	doc.HTTP.Routers = make(map[string]Router)
	doc.HTTP.Services = make(map[string]Service)

	rule := fmt.Sprintf("Host(`%s`)", route.Domain)
	if route.PathPrefix != "" {
		rule += fmt.Sprintf(" && PathPrefix(`%s`)", route.PathPrefix)
	}

	var tlsConf *struct {
		CertResolver string `yaml:"certResolver,omitempty"`
	}
	if route.Https {
		tlsConf = &struct {
			CertResolver string `yaml:"certResolver,omitempty"`
		}{
			CertResolver: "le", // Traefik's Let's Encrypt resolver name in CypherPanel
		}
	}

	doc.HTTP.Routers[appID] = Router{
		Rule:    rule,
		Service: appID,
		TLS:     tlsConf,
	}

	srv := Service{}
	srv.LoadBalancer.Servers = []struct {
		URL string `yaml:"url"`
	}{{URL: "http://" + upstream}}

	doc.HTTP.Services[appID] = srv

	b, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshaling route: %w", err)
	}

	cleanAppsDir := filepath.Clean(t.appsDir)
	finalPath := filepath.Clean(filepath.Join(cleanAppsDir, appID+".yml"))
	if !strings.HasPrefix(finalPath, cleanAppsDir) {
		return fmt.Errorf("invalid route path")
	}
	tmpPath := filepath.Join(cleanAppsDir, "."+appID+".yml.tmp")

	if err := os.WriteFile(tmpPath, b, 0644); err != nil {
		return fmt.Errorf("writing route tmp file: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming route file: %w", err)
	}

	return nil
}

// RemoveRoute deletes the Traefik fragment for an app.
func (t *TraefikWriter) RemoveRoute(ctx context.Context, appID string) error {
	if strings.Contains(appID, "..") || strings.Contains(appID, "/") || strings.Contains(appID, "\\") {
		return fmt.Errorf("invalid appID")
	}
	cleanAppsDir := filepath.Clean(t.appsDir)
	finalPath := filepath.Clean(filepath.Join(cleanAppsDir, appID+".yml"))
	if !strings.HasPrefix(finalPath, cleanAppsDir) {
		return fmt.Errorf("invalid route path")
	}
	if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing route file: %w", err)
	}
	return nil
}

// Route returns the currently configured upstream for an app, if any.
func (t *TraefikWriter) Route(ctx context.Context, appID string) (upstream string, ok bool, err error) {
	if strings.Contains(appID, "..") || strings.Contains(appID, "/") || strings.Contains(appID, "\\") {
		return "", false, fmt.Errorf("invalid appID")
	}
	cleanAppsDir := filepath.Clean(t.appsDir)
	finalPath := filepath.Clean(filepath.Join(cleanAppsDir, appID+".yml"))
	if !strings.HasPrefix(finalPath, cleanAppsDir) {
		return "", false, fmt.Errorf("invalid route path")
	}
	b, err := os.ReadFile(finalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("reading route file: %w", err)
	}

	var doc map[string]interface{}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "", false, fmt.Errorf("parsing route file: %w", err)
	}

	httpMap, ok := doc["http"].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	servicesMap, ok := httpMap["services"].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	appSrv, ok := servicesMap[appID].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	lb, ok := appSrv["loadBalancer"].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	servers, ok := lb["servers"].([]interface{})
	if !ok || len(servers) == 0 {
		return "", false, nil
	}
	srv, ok := servers[0].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	url, ok := srv["url"].(string)
	if !ok {
		return "", false, nil
	}

	url = strings.TrimPrefix(url, "http://")
	return url, true, nil
}
