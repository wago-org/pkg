package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/wago-org/registry-backend/internal/httpx"
	"github.com/wago-org/registry-backend/internal/model"
)

// handleListPackages returns the filtered, sorted package list with derived
// fields on each entry.
func (a *App) handleListPackages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pkgs := a.filterPackages(
		a.Store.ListPackages(),
		q.Get("q"), q.Get("category"), q.Get("tag"),
		q.Get("stability"), q.Get("engine"), q.Get("verified") == "true",
	)
	a.sortPackages(pkgs, q.Get("sort"))

	viewer := a.viewerID(r)
	out := make([]map[string]any, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, a.decoratePackage(p, viewer))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"packages": out, "total": len(out)})
}

// handleGetPackage returns a single package (matched by short or module name).
func (a *App) handleGetPackage(w http.ResponseWriter, r *http.Request) {
	p, ok := a.Store.GetPackage(r.PathValue("name"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "package not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.decoratePackage(p, a.viewerID(r)))
}

// handleVersions returns a package's versions, newest first.
func (a *App) handleVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := a.Store.GetPackage(r.PathValue("name"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "package not found")
		return
	}
	vs := append([]model.Version(nil), p.Versions...)
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].PublishedAt > vs[j].PublishedAt })
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"versions": vs})
}

// filterPackages applies the query, category, tag, stability, engine and
// verified filters.
func (a *App) filterPackages(pkgs []model.Package, q, category, tag, stability, engine string, verifiedOnly bool) []model.Package {
	q = strings.ToLower(strings.TrimSpace(q))
	category = strings.ToLower(strings.TrimSpace(category))
	tag = strings.ToLower(strings.TrimSpace(tag))
	stability = strings.ToLower(strings.TrimSpace(stability))

	out := make([]model.Package, 0, len(pkgs))
	for _, p := range pkgs {
		if verifiedOnly && !(p.Verified || p.Official) {
			continue
		}
		if category != "" && strings.ToLower(p.Category) != category {
			continue
		}
		if stability != "" && strings.ToLower(string(p.Stability)) != stability {
			continue
		}
		if tag != "" && !hasTag(p.Tags, tag) {
			continue
		}
		if engine != "" {
			if _, ok := p.Compat.Engines[engine]; !ok {
				continue
			}
		}
		if q != "" && !matchesQuery(p, q) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if strings.ToLower(t) == tag {
			return true
		}
	}
	return false
}

func matchesQuery(p model.Package, q string) bool {
	hay := strings.ToLower(strings.Join([]string{
		p.Name, p.Short, p.Description, p.Category,
		strings.Join(p.Tags, " "), strings.Join(p.Keywords, " "),
	}, " "))
	return strings.Contains(hay, q)
}

// sortPackages orders packages in place by mode: popular (installsWeek desc),
// quality (rating desc, then score desc), recent (updatedAt desc).
func (a *App) sortPackages(pkgs []model.Package, mode string) {
	switch mode {
	case "popular":
		sort.SliceStable(pkgs, func(i, j int) bool {
			return a.Store.InstallWeek(pkgs[i].Short) > a.Store.InstallWeek(pkgs[j].Short)
		})
	case "quality":
		sort.SliceStable(pkgs, func(i, j int) bool {
			if pkgs[i].Rating != pkgs[j].Rating {
				return pkgs[i].Rating > pkgs[j].Rating
			}
			return pkgs[i].Score > pkgs[j].Score
		})
	case "recent":
		sort.SliceStable(pkgs, func(i, j int) bool {
			return recency(pkgs[i]) > recency(pkgs[j])
		})
	}
}

// recency is the timestamp used to order by "recent": the latest version's
// publish time, falling back to the stored UpdatedAt.
func recency(p model.Package) string {
	if v := p.LatestVersion().PublishedAt; v != "" {
		return v
	}
	return p.UpdatedAt
}
