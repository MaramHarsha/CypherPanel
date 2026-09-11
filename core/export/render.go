package export

// The four things an export writes. Every one of them is derived from
// configuration the panel already holds; none of them can contain a secret,
// because this package has nothing that could unseal one (see export.go).

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// ─── README.md ──────────────────────────────────────────────────────────────

// readme is the part a human reads first, and it is generated per project
// rather than boilerplate. The four things the archive does NOT contain go at
// the top, plainly: an operator who discovers mid-recovery that their data was
// never in here has been misled by us (spec §3).
func (e *Exporter) readme(g gathered) string {
	var b strings.Builder
	name := g.project.Name
	fmt.Fprintf(&b, "# %s\n\n", name)
	fmt.Fprintf(&b, "Exported from CypherPanel %s. This archive runs anywhere Docker runs.\n\n", e.version)

	b.WriteString("## What this archive does not contain\n\n")
	b.WriteString("Read this first — all four are deliberate:\n\n")
	b.WriteString("1. **No secret values.** Every environment variable, database password,\n")
	b.WriteString("   webhook secret and credential stayed sealed in the panel. You get the\n")
	b.WriteString("   KEYS, in `env/*.env.example`, and you fill in the values.\n")
	b.WriteString("2. **No data.** Database contents and volume contents are not here. They\n")
	b.WriteString("   live on your servers; a Database Backup is how you move those.\n")
	b.WriteString("3. **No TLS certificates.** They are obtained per host from Let's Encrypt\n")
	b.WriteString("   and are not portable.\n")
	b.WriteString("4. **No images.** The compose files build from source or pull by\n")
	b.WriteString("   reference, exactly as the panel did.\n\n")

	b.WriteString("## Running it\n\n")
	for _, ed := range g.envs {
		dir := slug(ed.env.Name)
		fmt.Fprintf(&b, "### %s\n\n", ed.env.Name)
		fmt.Fprintf(&b, "```sh\ncd %s\n", dir)
		for _, ad := range ed.apps {
			fmt.Fprintf(&b, "cp env/%s.env.example env/%s.env   # then fill it in\n", slug(ad.app.Name), slug(ad.app.Name))
		}
		for _, db := range ed.dbs {
			fmt.Fprintf(&b, "cp env/%s.env.example env/%s.env   # then fill it in\n", slug(db.Name), slug(db.Name))
		}
		b.WriteString("docker compose up -d\n```\n\n")

		if len(ed.stacks) > 0 {
			b.WriteString("Compose Stacks in this environment run on their own:\n\n```sh\n")
			for _, sd := range ed.stacks {
				if sd.file != "" {
					fmt.Fprintf(&b, "docker compose -f stacks/%s.yml up -d\n", slug(sd.stack.Name))
				}
			}
			b.WriteString("```\n\n")
		}
	}

	// The routing table the archive cannot express for you (§3.5).
	routes := false
	for _, ed := range g.envs {
		for _, ad := range ed.apps {
			if ad.app.Route.Domain != "" {
				routes = true
			}
		}
		for _, sd := range ed.stacks {
			if sd.stack.Route.Domain != "" {
				routes = true
			}
		}
	}
	if routes {
		b.WriteString("## Routing\n\n")
		b.WriteString("The panel ran a managed Traefik and wrote its configuration from these\n")
		b.WriteString("domains. The compose files carry **no** proxy labels, because the proxy\n")
		b.WriteString("on the other side of this archive is yours to choose. Point it at:\n\n")
		b.WriteString("| Domain | Service | Port | HTTPS |\n|---|---|---|---|\n")
		for _, ed := range g.envs {
			for _, ad := range ed.apps {
				if ad.app.Route.Domain == "" {
					continue
				}
				fmt.Fprintf(&b, "| %s | %s | %d | %t |\n",
					ad.app.Route.Domain, slug(ad.app.Name), ad.app.Runtime.Port, ad.app.Route.HTTPS)
			}
			for _, sd := range ed.stacks {
				if sd.stack.Route.Domain == "" {
					continue
				}
				fmt.Fprintf(&b, "| %s | %s | %d | %t |\n",
					sd.stack.Route.Domain, sd.stack.Route.Service, sd.stack.Route.Port, sd.stack.Route.HTTPS)
			}
		}
		b.WriteString("\n")
	}

	// Volumes: the panel's name paired with compose's, because a rebuilt stack
	// gets new empty volumes unless someone deliberately points it at the old
	// ones.
	type vol struct{ panel, compose string }
	var vols []vol
	for _, ed := range g.envs {
		for _, ad := range ed.apps {
			for _, v := range ad.app.Volumes {
				vols = append(vols, vol{panel: panelVolumeName(ad.app.ID, v.Name), compose: slug(ad.app.Name) + "-" + slug(v.Name)})
			}
		}
		for _, db := range ed.dbs {
			vols = append(vols, vol{panel: db.VolumeName, compose: slug(db.Name) + "-data"})
		}
	}
	if len(vols) > 0 {
		b.WriteString("## Volumes\n\n")
		b.WriteString("These compose files declare **new, empty** volumes. If you are moving\n")
		b.WriteString("data, the panel's volume names on the old host were:\n\n")
		b.WriteString("| Compose volume | Panel volume on the server |\n|---|---|\n")
		for _, v := range vols {
			fmt.Fprintf(&b, "| %s | %s |\n", v.compose, v.panel)
		}
		b.WriteString("\n")
	}

	if len(g.shared) > 0 {
		b.WriteString("## Shared variables\n\n")
		b.WriteString("Some values referenced project-wide shared variables. Their keys:\n\n")
		for _, sv := range g.shared {
			scope := "project"
			if sv.EnvironmentID != nil {
				scope = "environment"
			}
			fmt.Fprintf(&b, "- `%s` (%s scope)\n", sv.Key, scope)
		}
		b.WriteString("\nThey are referenced in the `.env.example` files as comments beside the\nkeys that used them.\n")
	}
	return b.String()
}

// panelVolumeName mirrors the agent's deterministic naming, so the README can
// name the volume an operator will actually find on the old host.
func panelVolumeName(appID, name string) string {
	return fmt.Sprintf("cypher-%s-%s", appID, name)
}

// ─── cypherpanel.yaml ───────────────────────────────────────────────────────

// manifest is the lossless half: everything the panel knows that a compose
// file cannot express. Emitted by hand rather than through a YAML marshaller
// because key order is part of the determinism guarantee (§5) and a map would
// not preserve it.
func (e *Exporter) manifest(g gathered) string {
	var b strings.Builder
	b.WriteString("# The lossless half of this export: what the panel knew that a compose\n")
	b.WriteString("# file cannot say. Secret VALUES are absent by construction; keys are not\n")
	b.WriteString("# secret and are listed so you know what to fill in.\n")
	b.WriteString("export_version: 1\n")
	fmt.Fprintf(&b, "cypherpanel_version: %s\n", yamlStr(e.version))
	b.WriteString("project:\n")
	fmt.Fprintf(&b, "  id: %s\n", yamlStr(g.project.ID))
	fmt.Fprintf(&b, "  slug: %s\n", yamlStr(g.project.Slug))
	fmt.Fprintf(&b, "  name: %s\n", yamlStr(g.project.Name))
	fmt.Fprintf(&b, "  team: { id: %s, name: %s }\n", yamlStr(g.team.ID), yamlStr(g.team.Name))

	b.WriteString("environments:\n")
	for _, ed := range g.envs {
		fmt.Fprintf(&b, "  - name: %s\n", yamlStr(ed.env.Name))
		fmt.Fprintf(&b, "    kind: %s\n", yamlStr(ed.env.Kind))
		fmt.Fprintf(&b, "    network: %s\n", yamlStr("cypher-"+ed.env.ID))

		if len(ed.apps) > 0 {
			b.WriteString("    applications:\n")
			for _, ad := range ed.apps {
				a := ad.app
				fmt.Fprintf(&b, "      - name: %s\n", yamlStr(a.Name))
				fmt.Fprintf(&b, "        id: %s\n", yamlStr(a.ID))
				fmt.Fprintf(&b, "        server: { id: %s, name: %s }\n", yamlStr(a.Runtime.ServerID), yamlStr(ad.server.Name))
				b.WriteString("        source:\n")
				fmt.Fprintf(&b, "          kind: %s\n", yamlStr(a.Source.Kind))
				if a.Source.Kind == "image" {
					fmt.Fprintf(&b, "          image: %s\n", yamlStr(a.Source.Image))
				} else {
					fmt.Fprintf(&b, "          repo: %s\n", yamlStr(a.Source.Repo))
					fmt.Fprintf(&b, "          branch: %s\n", yamlStr(a.Source.Branch))
					// The commit that was actually serving is the single most
					// useful fact for someone rebuilding, so it is recorded —
					// while the compose file still pins the BRANCH, because a
					// git context resolved to a bare sha needs the host to
					// allow fetching an unreachable object, which most do not.
					fmt.Fprintf(&b, "          deployed_commit: %s\n", yamlStr(a.ObservedRevisionID))
					fmt.Fprintf(&b, "          build: { kind: %s, dockerfile: %s, context: %s }\n",
						yamlStr(a.Build.Kind), yamlStr(a.Build.DockerfilePath), yamlStr(a.Build.Context))
				}
				fmt.Fprintf(&b, "        runtime: { port: %d, replicas: %d }\n", a.Runtime.Port, a.Runtime.Replicas)
				fmt.Fprintf(&b, "        route: { domain: %s, https: %t, path_prefix: %s }\n",
					yamlStr(a.Route.Domain), a.Route.HTTPS, yamlStr(a.Route.PathPrefix))
				fmt.Fprintf(&b, "        health: { kind: %s, path: %s, interval_seconds: %d, timeout_seconds: %d, retries: %d }\n",
					yamlStr(a.Health.Kind), yamlStr(a.Health.Path), a.Health.IntervalSeconds, a.Health.TimeoutSeconds, a.Health.Retries)
				if len(a.Volumes) > 0 {
					b.WriteString("        volumes:\n")
					for _, v := range a.Volumes {
						fmt.Fprintf(&b, "          - { name: %s, path: %s, panel_volume: %s }\n",
							yamlStr(v.Name), yamlStr(v.Path), yamlStr(panelVolumeName(a.ID, v.Name)))
					}
				}
				if len(a.Ports) > 0 {
					b.WriteString("        ports:\n")
					for _, p := range a.Ports {
						fmt.Fprintf(&b, "          - { host_port: %d, container_port: %d, protocol: %s }\n",
							p.HostPort, p.ContainerPort, yamlStr(p.Protocol))
					}
				}
				if len(ad.envKeys) > 0 {
					keys := make([]string, 0, len(ad.envKeys))
					for _, k := range ad.envKeys {
						keys = append(keys, k.Key)
					}
					fmt.Fprintf(&b, "        env_keys: [%s]\n", strings.Join(keys, ", "))
					// shared_refs costs nothing to produce and is not secret:
					// it is already stored in cleartext precisely so no read
					// path has to unseal a value to answer "who uses this".
					b.WriteString(sharedRefsYAML(ad.envKeys))
				}
				if len(ad.tasks) > 0 {
					b.WriteString("        scheduled_tasks:\n")
					for _, t := range ad.tasks {
						fmt.Fprintf(&b, "          - { name: %s, schedule: %s, command: [%s], enabled: %t }\n",
							yamlStr(t.Name), yamlStr(t.Schedule), quotedList(t.Command), t.Enabled)
					}
				}
			}
		}

		if len(ed.dbs) > 0 {
			b.WriteString("    databases:\n")
			for _, db := range ed.dbs {
				fmt.Fprintf(&b, "      - name: %s\n", yamlStr(db.Name))
				fmt.Fprintf(&b, "        engine: %s\n", yamlStr(string(db.Engine)))
				fmt.Fprintf(&b, "        version: %s\n", yamlStr(db.Version))
				fmt.Fprintf(&b, "        initial_database: %s\n", yamlStr(db.InitialDatabase))
				fmt.Fprintf(&b, "        internal_host: %s\n", yamlStr("cypher-db-"+db.ID))
				fmt.Fprintf(&b, "        panel_volume: %s\n", yamlStr(db.VolumeName))
			}
		}

		if len(ed.stacks) > 0 {
			b.WriteString("    compose_stacks:\n")
			for _, sd := range ed.stacks {
				fmt.Fprintf(&b, "      - name: %s\n", yamlStr(sd.stack.Name))
				fmt.Fprintf(&b, "        file: %s\n", yamlStr(slug(ed.env.Name)+"/stacks/"+slug(sd.stack.Name)+".yml"))
				fmt.Fprintf(&b, "        route: { domain: %s, service: %s, port: %d, https: %t }\n",
					yamlStr(sd.stack.Route.Domain), yamlStr(sd.stack.Route.Service), sd.stack.Route.Port, sd.stack.Route.HTTPS)
				if len(sd.envKeys) > 0 {
					fmt.Fprintf(&b, "        env_keys: [%s]\n", strings.Join(sd.envKeys, ", "))
				}
			}
		}
	}

	if len(g.shared) > 0 {
		b.WriteString("shared_variables:\n")
		for _, sv := range g.shared {
			scope := "project"
			if sv.EnvironmentID != nil {
				scope = "environment"
			}
			fmt.Fprintf(&b, "  - { key: %s, scope: %s }\n", yamlStr(sv.Key), scope)
		}
	}
	return b.String()
}

func sharedRefsYAML(keys []domain.EnvVarKey) string {
	var withRefs []domain.EnvVarKey
	for _, k := range keys {
		if len(k.SharedRefs) > 0 {
			withRefs = append(withRefs, k)
		}
	}
	if len(withRefs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("        shared_refs:\n")
	for _, k := range withRefs {
		refs := append([]string(nil), k.SharedRefs...)
		sort.Strings(refs) // sorted, because determinism (§5)
		fmt.Fprintf(&b, "          %s: [%s]\n", k.Key, strings.Join(refs, ", "))
	}
	return b.String()
}

// ─── docker-compose.yml ─────────────────────────────────────────────────────

// compose is the portable half. It carries NO Traefik labels: the panel drove
// its proxy from the file provider (ADR-004), and the proxy on the other side
// of this archive is the reader's to choose — so the routing table goes in the
// README instead, where a human can act on it (§3.5).
func (e *Exporter) compose(ed envData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — exported from CypherPanel.\n", ed.env.Name)
	b.WriteString("#\n# No proxy labels: point your own proxy at the table in ../README.md.\n")
	b.WriteString("# The env files this references are NOT in the archive — copy each\n")
	b.WriteString("# .env.example, fill it in, and rename it. `docker compose up` fails\n")
	b.WriteString("# naming the missing file, which is the remedy.\n")
	b.WriteString("services:\n")

	for _, ad := range ed.apps {
		a := ad.app
		name := slug(a.Name)
		fmt.Fprintf(&b, "  %s:\n", name)
		if a.Source.Kind == "image" {
			fmt.Fprintf(&b, "    image: %s\n", yamlStr(a.Source.Image))
		} else {
			// A git remote context, pinned to the branch — see the manifest's
			// note on why not the commit.
			fmt.Fprintf(&b, "    build:\n      context: %s\n", yamlStr(gitContext(a)))
			if a.Build.DockerfilePath != "" {
				fmt.Fprintf(&b, "      dockerfile: %s\n", yamlStr(strings.TrimPrefix(a.Build.DockerfilePath, "./")))
			}
		}
		b.WriteString("    restart: unless-stopped\n")
		fmt.Fprintf(&b, "    env_file: [./env/%s.env]\n", name)
		if len(a.Ports) > 0 {
			b.WriteString("    ports:\n")
			for _, p := range a.Ports {
				fmt.Fprintf(&b, "      - %s\n", yamlStr(fmt.Sprintf("%d:%d/%s", p.HostPort, p.ContainerPort, p.Protocol)))
			}
		}
		if len(a.Volumes) > 0 {
			b.WriteString("    volumes:\n")
			for _, v := range a.Volumes {
				fmt.Fprintf(&b, "      - %s\n", yamlStr(name+"-"+slug(v.Name)+":"+v.Path))
			}
		}
		if a.Health.Kind == "http" && a.Health.Path != "" {
			b.WriteString("    healthcheck:\n")
			fmt.Fprintf(&b, "      test: [\"CMD-SHELL\", \"wget -q -O- http://localhost:%d%s || exit 1\"]\n",
				a.Runtime.Port, a.Health.Path)
			fmt.Fprintf(&b, "      interval: %ds\n      timeout: %ds\n      retries: %d\n",
				a.Health.IntervalSeconds, a.Health.TimeoutSeconds, a.Health.Retries)
		}
	}

	for _, db := range ed.dbs {
		name := slug(db.Name)
		fmt.Fprintf(&b, "  %s:\n", name)
		fmt.Fprintf(&b, "    image: %s\n", yamlStr(engineImage(db)))
		b.WriteString("    restart: unless-stopped\n")
		fmt.Fprintf(&b, "    env_file: [./env/%s.env]\n", name)
		b.WriteString("    volumes:\n")
		fmt.Fprintf(&b, "      - %s\n", yamlStr(name+"-data:"+db.DataPath))
		// The alias is what makes existing connection strings survive: the
		// panel's internal hostname was cypher-db-<id>, and an application's
		// DATABASE_URL still says so (§3.3).
		b.WriteString("    networks:\n      default:\n        aliases:\n")
		fmt.Fprintf(&b, "          - %s\n", yamlStr("cypher-db-"+db.ID))
	}

	// Named volumes, declared so compose creates them fresh. The README pairs
	// each with the panel's own name on the old host.
	var vols []string
	for _, ad := range ed.apps {
		for _, v := range ad.app.Volumes {
			vols = append(vols, slug(ad.app.Name)+"-"+slug(v.Name))
		}
	}
	for _, db := range ed.dbs {
		vols = append(vols, slug(db.Name)+"-data")
	}
	if len(vols) > 0 {
		sort.Strings(vols)
		b.WriteString("volumes:\n")
		for _, v := range vols {
			fmt.Fprintf(&b, "  %s: {}\n", v)
		}
	}
	return b.String()
}

// gitContext renders the source as a compose build context. Compose understands
// a git URL with a branch fragment directly.
func gitContext(a domain.ApplicationConfig) string {
	repo := a.Source.Repo
	if a.Source.Branch != "" {
		return repo + "#" + a.Source.Branch
	}
	return repo
}

// engineImage names the upstream image for an engine. The panel pins digests
// internally; the archive names a readable tag, because an operator rebuilding
// on another host wants something they can reason about and update.
func engineImage(db domain.DatabaseConfig) string {
	v := db.Version
	if v == "" {
		v = "latest"
	}
	switch string(db.Engine) {
	case "postgresql":
		return "postgres:" + v
	case "mysql":
		return "mysql:" + v
	case "mariadb":
		return "mariadb:" + v
	case "mongodb":
		return "mongo:" + v
	case "redis":
		return "redis:" + v
	case "valkey":
		return "valkey/valkey:" + v
	default:
		return string(db.Engine) + ":" + v
	}
}

// ─── env/*.env.example ──────────────────────────────────────────────────────

func appEnvExample(ad appData) string {
	var b strings.Builder
	b.WriteString("# CypherPanel exported the KEYS of this resource's environment. Values\n")
	b.WriteString("# stay sealed in the panel and were not exported. Fill them in and rename\n")
	fmt.Fprintf(&b, "# this file to %s.env.\n", slug(ad.app.Name))
	for _, k := range ad.envKeys {
		if len(k.SharedRefs) > 0 {
			refs := append([]string(nil), k.SharedRefs...)
			sort.Strings(refs)
			fmt.Fprintf(&b, "%s=      # built from %s\n", k.Key, "{{shared."+strings.Join(refs, "}}, {{shared.")+"}}")
			continue
		}
		fmt.Fprintf(&b, "%s=\n", k.Key)
	}
	return b.String()
}

// dbEnvExample carries the one sentence an operator will not otherwise expect:
// a new password does not open an existing data directory.
func dbEnvExample(db domain.DatabaseConfig) string {
	var b strings.Builder
	b.WriteString("# CypherPanel exported the KEYS of this database's environment. The root\n")
	b.WriteString("# password stayed sealed in the panel and was not exported.\n")
	b.WriteString("#\n")
	b.WriteString("# Note: a NEW password does not open an EXISTING data directory. This\n")
	b.WriteString("# compose file declares a fresh, empty volume, so the engine will\n")
	b.WriteString("# initialize with whatever you set here. To recover the old contents,\n")
	b.WriteString("# restore from a Database Backup instead.\n")
	switch string(db.Engine) {
	case "postgresql":
		b.WriteString("POSTGRES_PASSWORD=\n")
		b.WriteString("POSTGRES_USER=" + db.RootUser + "\n")
		if db.InitialDatabase != "" {
			b.WriteString("POSTGRES_DB=" + db.InitialDatabase + "\n")
		}
	case "mysql", "mariadb":
		b.WriteString("MYSQL_ROOT_PASSWORD=\n")
		if db.InitialDatabase != "" {
			b.WriteString("MYSQL_DATABASE=" + db.InitialDatabase + "\n")
		}
	case "mongodb":
		b.WriteString("MONGO_INITDB_ROOT_PASSWORD=\n")
		b.WriteString("MONGO_INITDB_ROOT_USERNAME=" + db.RootUser + "\n")
	case "redis", "valkey":
		b.WriteString("REDIS_PASSWORD=\n")
	}
	return b.String()
}

func stackEnvExample(sd stackData) string {
	var b strings.Builder
	b.WriteString("# CypherPanel exported the KEYS this stack's compose file interpolates.\n")
	b.WriteString("# Values stay sealed in the panel and were not exported.\n")
	for _, k := range sd.envKeys {
		fmt.Fprintf(&b, "%s=\n", k)
	}
	return b.String()
}

// ─── small helpers ──────────────────────────────────────────────────────────

// yamlStr quotes a scalar so a value containing a colon, a hash or a leading
// digit cannot change the document's shape.
func yamlStr(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func quotedList(items []string) string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, yamlStr(s))
	}
	return strings.Join(out, ", ")
}
