package server

import (
	"context"
	"net/http"

	"github.com/dsjodin/labprovider/services/control-plane/internal/access"
	"github.com/dsjodin/labprovider/services/control-plane/internal/deploy"
	"github.com/dsjodin/labprovider/services/control-plane/internal/envfile"
)

// servicePageLogTail is how much of each container's log the page shows. Enough
// to see why something is unhealthy, short enough that a service with four
// containers is still one screen. The log viewer is where you go for more.
const servicePageLogTail = 40

// ServicePage is one service in full: what the dashboard card summarizes, plus
// the configuration that drives it, its containers, and their recent output.
// Every write it offers already existed as an API endpoint.
type ServicePage struct {
	Chrome
	Row    ServiceRow
	Deps   []string
	Access *access.Entry
	Vars   []ConfigVar
	Logs   []ContainerLog
	// Depot enables the URL-fetch section. Only the depot has somewhere to
	// put a downloaded bundle, so only its page offers one.
	Depot  bool
	Status Status // Docker's availability, which decides how much of the page is real
}

// ConfigVar is one schema variable the service is deployed from. Generated is
// true only when the variable is empty *and* generated, which is how an
// operator asks for a value rather than a value being missing.
type ConfigVar struct {
	Name      string
	Value     string
	Secret    bool
	Generated bool
}

type ContainerLog struct {
	Container string
	Lines     []string
	Err       string
}

// LogTail lets the template state the tail size it is showing without the
// constant being duplicated in markup.
func (ServicePage) LogTail() int { return servicePageLogTail }

func (s *Server) handleServicePage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var svc deploy.Service
	for _, candidate := range s.opt.Engine.Services() {
		if candidate.Name() == name {
			svc = candidate
			break
		}
	}
	if svc == nil {
		http.NotFound(w, r)
		return
	}

	panel, _ := s.collectServices(r.Context(), s.opt.Now())
	page := ServicePage{
		Chrome: s.chrome(name, "services"),
		Deps:   svc.Deps(),
		Status: panel.Status,
		// A service Docker cannot answer for still renders: the configuration
		// and the deploy history are local, and they are what an operator
		// looking at a broken lab needs.
		Row:   ServiceRow{Name: name, State: stateNotDeployed, Core: isFoundation(name)},
		Depot: name == "depot",
	}
	for _, row := range panel.Services {
		if row.Name == name {
			page.Row = row
			break
		}
	}

	if content, saved, err := s.opt.Engine.Store.Load(); err == nil && saved {
		env := envfile.Parse(content)
		for _, entry := range access.Build(env) {
			if entry.Service == name {
				e := entry
				page.Access = &e
				break
			}
		}
		for _, v := range envfile.VariablesFor(name) {
			page.Vars = append(page.Vars, ConfigVar{
				Name:      v,
				Value:     env[v],
				Secret:    envfile.IsSecret(v),
				Generated: env[v] == "" && envfile.IsGenerated(v),
			})
		}
	}

	if s.opt.Docker != nil {
		for _, c := range page.Row.Containers {
			entry := ContainerLog{Container: c.Name}
			lctx, cancel := context.WithTimeout(r.Context(), s.opt.Timeout)
			lines, err := s.opt.Docker.LogLines(lctx, c.ID, servicePageLogTail)
			cancel()
			if err != nil {
				entry.Err = err.Error()
			} else {
				entry.Lines = lines
			}
			page.Logs = append(page.Logs, entry)
		}
	}

	s.render(w, s.pages["service.html"], "layout", page)
}
