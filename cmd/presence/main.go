package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

const expiry = 90 * time.Second

type entry struct {
	Name string    `json:"name"`
	Last time.Time `json:"-"`
}

var (
	mu      sync.Mutex
	users   = map[string]*entry{}
)

func main() {
	http.HandleFunc("/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "missing name", 400)
			return
		}

		mu.Lock()
		// Prune expired
		now := time.Now()
		for k, e := range users {
			if now.Sub(e.Last) > expiry {
				delete(users, k)
			}
		}
		users[name] = &entry{Name: name, Last: now}

		// Build response
		list := make([]string, 0, len(users))
		for _, e := range users {
			list = append(list, e.Name)
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})

	log.Println("Presence server on :9090")
	log.Fatal(http.ListenAndServe("127.0.0.1:9090", nil))
}
