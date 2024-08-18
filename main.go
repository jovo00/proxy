package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"log"
	"net/http"
	"os"
	"proxy/balancer"
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/models"
)

func Skipper(c echo.Context, adminHost string) bool {
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
		return nil
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
