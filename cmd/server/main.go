// Command server boots the Instagram brand account management panel: REST
// API, WebSocket hub, background job engine, and serves the web panel as
// static files.
package main

import (
	"log"
	"net/http"

	"instagrampanel/internal/api"
	"instagrampanel/internal/auth"
	"instagrampanel/internal/config"
	"instagrampanel/internal/crypto"
	"instagrampanel/internal/db"
	"instagrampanel/internal/instagram"
	"instagrampanel/internal/queue"
	"instagrampanel/internal/realtime"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	gdb, err := db.Open(cfg.DBDriver, cfg.DBDSN)
	if err != nil {
		log.Fatalf("database: %v", err)
	}

	box, err := crypto.New(cfg.EncryptionKey)
	if err != nil {
		log.Fatalf("crypto: %v", err)
	}

	authSvc := auth.NewService(cfg.JWTSecret)
	var bridge *instagram.Bridge
	if cfg.BridgeURL != "" {
		bridge = instagram.NewBridge(cfg.BridgeURL, cfg.BridgeToken)
	} else if bridge, err = instagram.StartBridge(cfg.BridgePython, "bridge/bridge.py"); err != nil {
		// Keep the panel up so the problem is visible; Instagram calls report it.
		log.Printf("instagram bridge: %v", err)
	}
	igManager := instagram.NewManager(gdb, box, bridge, cfg.ProxyCheckURL)
	hub := realtime.NewHub(cfg.CORSOrigin)
	queueManager := queue.NewManager(gdb, igManager, hub)

	server := api.NewServer(gdb, box, authSvc, igManager, queueManager, hub, cfg.CORSOrigin)

	log.Printf("listening on %s (db=%s)", cfg.HTTPAddr, cfg.DBDriver)
	if err := http.ListenAndServe(cfg.HTTPAddr, server.Router("web")); err != nil {
		log.Fatal(err)
	}
}
