package main

import (
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"mediaflow/gateway/background"
	"mediaflow/gateway/db"
	"mediaflow/gateway/routes"
	"mediaflow/gateway/services"
)

func main() {
	dbPath := os.Getenv("DATABASE_PATH")
	if dbPath == "" {
		dbPath = "/data/mediaflow.db"
	}

	if err := db.Init(dbPath); err != nil {
		log.Fatalf("Database init failed: %v", err)
	}
	log.Println("Database initialized")

	if err := services.InitStorage(); err != nil {
		log.Fatalf("Storage init failed: %v", err)
	}
	log.Println("Storage service initialized")

	if err := services.InitQueue(); err != nil {
		log.Fatalf("Queue init failed: %v", err)
	}
	log.Println("Queue service initialized")

	go background.StartWatchdog()
	go background.StartQueueMonitor()

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	routes.RegisterJobRoutes(r)
	routes.RegisterInternalRoutes(r)
	routes.RegisterAdminRoutes(r)

	log.Println("Gateway started — watchdog and monitor loops running")
	if err := http.ListenAndServe("0.0.0.0:8000", r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
