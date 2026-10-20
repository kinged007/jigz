package main

import (
	"log"
	"net/http"
	"time"

	"github.com/kinged007/sortie-loop/internal/auth"
)

const (
	envPassword = "SORTIE_CENTRAL_PASSWORD"
	envSecret   = "SORTIE_CENTRAL_SESSION_SECRET"
)

// setupAuth builds the access control from the loaded config and the
// password and session secret taken from the environment.
func (a *app) setupAuth(password, secretEnv string) error {
	a.mu.Lock()
	access, dir := a.access, a.stateDir
	a.mu.Unlock()
	cfg := auth.Config{
		Password:     password,
		PublicRead:   access.PublicRead,
		TrustedProxy: access.TrustedProxy,
		LoginPage:    loginPage,
		OnEvent: func(actor, action, result string) {
			a.auditAs(actor, action, "", "", result)
		},
	}
	if password != "" {
		secret, err := auth.LoadSecret(dir, secretEnv)
		if err != nil {
			return err
		}
		cfg.Secret = secret
	}
	a.auth = auth.New(cfg)
	return nil
}

func loginPage(w http.ResponseWriter, d auth.LoginData) {
	if err := page.ExecuteTemplate(w, "login", d); err != nil {
		log.Printf("login: %v", err)
	}
}

// audit records a change made by the client of r.
func (a *app) audit(r *http.Request, action, project, target, result string) {
	a.auditAs(auth.Session(r).Actor, action, project, target, result)
}

func (a *app) auditAs(actor, action, project, target, result string) {
	if a.store == nil {
		return
	}
	if err := a.store.AddAudit(time.Now(), actor, action, project, target, result); err != nil {
		log.Printf("audit: %v", err)
	}
}
