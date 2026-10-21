package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"example.com/legal-tenant-service/internal/infrai"
	"example.com/legal-tenant-service/internal/legal"
)

func main() {
	setup := flag.Bool("setup", false, "create the document bucket before serving")
	flag.Parse()
	key := requiredEnv("INFRAI_API_KEY")
	bucket := envOr("LEGAL_BUCKET", "legal-signed-documents")
	client := infrai.NewClient(key, nil)
	if *setup {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.CreateBucket(ctx, bucket); err != nil {
			log.Fatal(err)
		}
		log.Printf("created bucket %s", bucket)
		return
	}

	service := legal.NewService(client, requiredEnv("TENANT_ROOT_DOMAIN"), requiredEnv("SERVICE_CNAME_TARGET"), bucket)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /matters", func(w http.ResponseWriter, r *http.Request) {
		var input legal.Intake
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		matter, err := service.Intake(r.Context(), input)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, matter)
	})
	mux.HandleFunc("GET /matters/{matterID}/signed", func(w http.ResponseWriter, r *http.Request) {
		url, err := service.SignedDownload(r.Context(), tenantFromHost(r.Host), r.PathValue("matterID"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		http.Redirect(w, r, url, http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /assets/{key}", func(w http.ResponseWriter, r *http.Request) {
		url, err := service.TenantAsset(r.Context(), tenantFromHost(r.Host), r.PathValue("key"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		http.Redirect(w, r, url, http.StatusTemporaryRedirect)
	})

	addr := envOr("LISTEN_ADDR", ":8080")
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("legal tenant service listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}

func tenantFromHost(host string) string {
	host = strings.Split(host, ":")[0]
	return strings.Split(host, ".")[0]
}

func writeServiceError(w http.ResponseWriter, err error) {
	var apiErr *infrai.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
		writeError(w, apiErr.StatusCode, apiErr.Message)
		return
	}
	writeError(w, http.StatusBadGateway, err.Error())
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func requiredEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
