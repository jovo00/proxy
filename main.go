package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"proxy/balancer"
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/models"
)

func Skipper(c echo.Context, adminHost string) bool {
	fmt.Println(c.Request().Host)
	return c.Request().Host == adminHost
}

func ModifyResponse(res *http.Response, customBalancer *balancer.CustomBalancer, adminHost string) error {
	if strings.Contains(res.Header.Get("Content-Type"), "text/html") {
		record := customBalancer.GetRecord(res.Request.Host)
		customCss := record.GetString("custom_css")
		customFavicon := record.GetString("custom_favicon")

		if customCss == "" && customFavicon == "" {
			return nil
		}

		var body []byte
		var err error

		if res.Header.Get("Content-Encoding") == "gzip" {
			gzReader, err := gzip.NewReader(res.Body)
			if err != nil {
				return err
			}
			defer gzReader.Close()
			body, err = io.ReadAll(gzReader)
			if err != nil {
				return err
			}
		} else {
			body, err = io.ReadAll(res.Body)
			if err != nil {
				return err
			}
		}
		defer res.Body.Close()

		modifiedBody := string(body)
		if customCss != "" {
			cssFilePath := "/api/files/" + record.BaseFilesPath() + "/" + customCss
			modifiedBody = strings.Replace(modifiedBody, "</head>", `<link rel="stylesheet" type="text/css" href="https://`+adminHost+"/"+cssFilePath+`"></head>`, 1)
		}
		if customFavicon != "" {
			faviconFilePath := "/api/files/" + record.BaseFilesPath() + "/" + customFavicon
			re := regexp.MustCompile(`<link rel="icon"[^>]*>`)
			modifiedBody = re.ReplaceAllString(modifiedBody, "")
			modifiedBody = strings.Replace(modifiedBody, "</head>", `<link rel="icon" type="image/x-icon" href="https://`+adminHost+"/"+faviconFilePath+`"></head>`, 1)
		}

		var buf bytes.Buffer
		if res.Header.Get("Content-Encoding") == "gzip" {
			gzWriter := gzip.NewWriter(&buf)
			_, err = gzWriter.Write([]byte(modifiedBody))
			if err != nil {
				return err
			}
			gzWriter.Close()
		} else {
			buf.WriteString(modifiedBody)
		}

		res.Body = io.NopCloser(&buf)
		res.ContentLength = int64(buf.Len())
		res.Header.Set("Content-Length", strconv.Itoa(buf.Len()))
	}

	return nil
}

func main() {
	if err := godotenv.Load(); err == nil {
		log.Println("Reading environment variables from .env")
	}

	app := pocketbase.New()
	adminHost := os.Getenv("HOST")
	if adminHost == "" {
		log.Fatal("HOST environment variable is required")
	}

	customBalancer := &balancer.CustomBalancer{
		App:     app,
		Targets: map[string]models.Record{},
		Index:   map[string]string{},
	}

	app.OnRecordAfterCreateRequest("proxies").Add(func(e *core.RecordCreateEvent) error {
		customBalancer.CreateTarget(e.Record)
		return nil
	})
	app.OnRecordAfterUpdateRequest("proxies").Add(func(e *core.RecordUpdateEvent) error {
		customBalancer.UpdateTarget(e.Record)
		return nil
	})
	app.OnRecordAfterDeleteRequest("proxies").Add(func(e *core.RecordDeleteEvent) error {
		customBalancer.DeleteTarget(e.Record)
		return nil
	})

	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {

		records, _ := app.Dao().FindRecordsByFilter("proxies", "id != null", "id", 5000, 0, nil)
		for _, record := range records {
			customBalancer.CreateTarget(record)
		}

		e.Router.Use(middleware.ProxyWithConfig(middleware.ProxyConfig{
			Skipper:        func(c echo.Context) bool { return Skipper(c, adminHost) },
			Balancer:       customBalancer,
			ModifyResponse: func(res *http.Response) error { return ModifyResponse(res, customBalancer, adminHost) },
		}))

		e.Router.GET("/dns/update", func(c echo.Context) error {
			user := c.QueryParam("username")
			password := c.QueryParam("password")
			domain := c.QueryParam("domain")
			ip := c.QueryParam("ip")
			ip6 := c.QueryParam("ip6")

			if user == "" || password == "" || domain == "" || (ip == "" && ip6 == "") {
				return c.JSON(http.StatusBadRequest, map[string]string{"message": "Invalid request"})
			}

			record, err := app.Dao().FindFirstRecordByFilter(
				"dns", "username = {:user} && password = {:password} && domain = {:domain}",
				dbx.Params{"user": user, "password": password, "domain": domain},
			)
			if err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"message": "Invalid request"})
			}

			records, err := app.Dao().FindRecordsByFilter("proxies", "dns = {:dns}", "id", 1000, 0, dbx.Params{"dns": record.Id})
			if err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"message": "Invalid request"})
			}

			for _, record := range records {
				currentTarget := record.GetString("target")
				port := 0

				// check if the target has a port
				_, portStr, err := net.SplitHostPort(currentTarget)
				if err == nil {
					port, _ = strconv.Atoi(portStr)
				}

				if record.GetBool("dns_ipv6") && ip6 != "" {
					if port > 0 {
						record.Set("target", "["+ip6+"]:"+strconv.Itoa(port))
					} else {
						record.Set("target", "["+ip6+"]")
					}
				} else if ip != "" {
					if port > 0 {
						record.Set("target", ip+":"+strconv.Itoa(port))
					} else {
						record.Set("target", ip)
					}
				}

				if err := app.Dao().SaveRecord(record); err != nil {
					return err
				}
			}

			return c.JSON(http.StatusOK, map[string]string{"message": "Success"})
		} /* optional middlewares */)

		return nil
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
