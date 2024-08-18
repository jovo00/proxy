package balancer

import (
	"net/url"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/models"
)

type Target struct {
	Id     string
	Route  string
	Target string
}

type CustomBalancer struct {
	App     *pocketbase.PocketBase
	Targets map[string]models.Record
	Index   map[string]string
}

func (cb *CustomBalancer) CreateTarget(record *models.Record) {
	cb.Targets[record.Id] = *record
	cb.RebuildIndex()
}

func (cb *CustomBalancer) UpdateTarget(record *models.Record) {
	cb.Targets[record.Id] = *record
	cb.RebuildIndex()
}

func (cb *CustomBalancer) DeleteTarget(record *models.Record) {
	delete(cb.Targets, record.Id)
	cb.RebuildIndex()
}

func (cb *CustomBalancer) RebuildIndex() {
	for _, record := range cb.Targets {
		cb.Index[record.GetString("host")] = record.Id
	}
}

func (cb *CustomBalancer) IsRecursive(host string) bool {
	_, ok := cb.Index[host]

	return ok
}

func (cb *CustomBalancer) GetRecord(host string) *models.Record {
	id, ok := cb.Index[host]
	if !ok {
		return nil
	}
	record, ok := cb.Targets[id]
	if !ok {
		return nil
	}
	return &record
}

func (cb *CustomBalancer) AddTarget(target *middleware.ProxyTarget) bool {
	// No-op or implement add target logic if needed
	return false
}

func (cb *CustomBalancer) RemoveTarget(name string) bool {
	// No-op or implement remove target logic if needed
	return false
}

func (cb *CustomBalancer) Next(c echo.Context) (*middleware.ProxyTarget, error) {
	record := cb.GetRecord(c.Request().Host)
	if record == nil {
		return nil, echo.NewHTTPError(404, "Not found")
	}

	scheme := c.Scheme()
	if scheme == "" || (scheme == "http" && record.GetBool("https")) {
		scheme = "https"
	}

	if !record.GetBool("https") {
		scheme = "http"
	}

	target := record.GetString("target")
	if cb.IsRecursive(target) {
		return nil, echo.NewHTTPError(500, "Recursive target")
	}

	targetUrl := scheme + "://" + record.GetString("target")

	parsedURL, err := url.Parse(targetUrl)
	if err != nil {
		return nil, err
	}

	return &middleware.ProxyTarget{
		URL: parsedURL,
	}, nil
}
