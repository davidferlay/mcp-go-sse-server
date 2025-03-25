// main.go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Example Tools
// 1. Echo Tool: returns the message sent in the arguments.
// 2. Math Tool: supports simple math operations (addition and subtraction).
func echoTool(args map[string]interface{}) (interface{}, error) {
	message, ok := args["message"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'message' field")
	}
	return message, nil
}

func mathTool(args map[string]interface{}) (interface{}, error) {
	op, ok := args["operation"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'operation' field")
	}
	a, okA := args["a"].(float64)
	b, okB := args["b"].(float64)
	if !okA || !okB {
		return nil, fmt.Errorf("invalid or missing numbers for 'a' or 'b'")
	}
	switch op {
	case "add":
		return a + b, nil
	case "sub":
		return a - b, nil
	default:
		return nil, fmt.Errorf("unsupported operation: %s", op)
	}
}

// Example Resources
// 1. Welcome Resource: responds with a welcome JSON message.
// 2. Time Resource: responds with the current server time.
func welcomeResource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "Welcome to the MCP Server!"})
}

func timeResource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"time": time.Now().Format(time.RFC3339)})
}

// SSE Broker: manages connected clients and broadcasts events.
var (
	clients        = make(map[chan string]bool)
	clientsChan    = make(chan chan string)
	unregisterChan = make(chan chan string)
	broadcastChan  = make(chan string)
)

func sseBroker() {
	for {
		select {
		case client := <-clientsChan:
			clients[client] = true
		case client := <-unregisterChan:
			if _, ok := clients[client]; ok {
				delete(clients, client)
				close(client)
			}
		case message := <-broadcastChan:
			for client := range clients {
				// Non-blocking send to each client.
				select {
				case client <- message:
				default:
				}
			}
		}
	}
}

// sseHandler implements the Server-Sent Events (SSE) endpoint.
func sseHandler(w http.ResponseWriter, r *http.Request) {
	// Set required SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	// Generate a session id.
	sessionId := uuid.New().String()

	// Send an initial event to the client with the endpoint for posting messages.
	fmt.Fprintf(w, "event: endpoint\n")
	fmt.Fprintf(w, "data: /messages?sessionId=%s\n\n", sessionId)
	flusher.Flush()

	// Create a channel for this client and register it.
	clientChan := make(chan string)
	clientsChan <- clientChan
	defer func() {
		unregisterChan <- clientChan
	}()

	// Setup a heartbeat ticker to send periodic heartbeat events.
	heartbeatTicker := time.NewTicker(30 * time.Second)
	defer heartbeatTicker.Stop()

	notify := r.Context().Done()
	for {
		select {
		case msg, ok := <-clientChan:
			if !ok {
				return
			}
			// Write the broadcast message in SSE format.
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-heartbeatTicker.C:
			// Send a heartbeat event to keep the connection alive.
			fmt.Fprintf(w, "event: heartbeat\n")
			fmt.Fprintf(w, "data: keepalive\n\n")
			flusher.Flush()
		case <-notify:
			return
		}
	}
}

// messagesHandler receives POST requests from SSE clients.
// It checks for JSON-RPC requests (like "initialize") and replies appropriately.
func messagesHandler(w http.ResponseWriter, r *http.Request) {
	// Only accept POST requests.
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract sessionId from query parameters.
	sessionId := r.URL.Query().Get("sessionId")
	if sessionId == "" {
		http.Error(w, "Missing sessionId", http.StatusBadRequest)
		return
	}

	var requestPayload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
		http.Error(w, fmt.Sprintf("Invalid message payload: %v", err), http.StatusBadRequest)
		return
	}
	log.Printf("Received message for session %s: %v", sessionId, requestPayload)

	// If this is a JSON-RPC request, respond with the expected structure.
	if _, ok := requestPayload["jsonrpc"]; ok {
		// Use the same "id" from the request.
		id := requestPayload["id"]
		// Here, we simply respond with an empty result.
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  map[string]interface{}{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	// Otherwise, respond with a generic confirmation.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "received"})
}

// mcpHandler decodes an incoming JSON request for a tool, executes it,
// broadcasts the result via SSE, and responds with an acceptance status.
func mcpHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tool string                 `json:"tool"`
		Args map[string]interface{} `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	var (
		result interface{}
		err    error
	)
	switch req.Tool {
	case "echo":
		result, err = echoTool(req.Args)
	case "math":
		result, err = mathTool(req.Args)
	default:
		http.Error(w, "unknown tool", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("error processing tool: %v", err), http.StatusInternalServerError)
		return
	}

	// Prepare the result as JSON and broadcast it to SSE clients.
	resBody, err := json.Marshal(map[string]interface{}{"result": result})
	if err != nil {
		http.Error(w, fmt.Sprintf("error marshalling result: %v", err), http.StatusInternalServerError)
		return
	}
	broadcastChan <- string(resBody)

	// Respond to the HTTP caller.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "tool executed, result broadcast via SSE"})
}

func main() {
	// Command-line flag for the port (default is 3001)
	portFlag := flag.String("port", "3001", "Port to run the MCP server on.")
	flag.Parse()
	port := *portFlag

	// Start the SSE broker.
	go sseBroker()

	// Register endpoints.
	http.HandleFunc("/mcp", mcpHandler)
	http.HandleFunc("/welcome", welcomeResource)
	http.HandleFunc("/time", timeResource)
	http.HandleFunc("/sse", sseHandler)
	http.HandleFunc("/messages", messagesHandler)

	// Log example prompts.
	log.Printf("MCP SSE Server started on :%s", port)
	log.Println("Example prompts:")
	log.Println(`1. {"tool": "echo", "args": {"message": "Hello MCP Server!"}}`)
	log.Println(`2. {"tool": "math", "args": {"operation": "add", "a": 5, "b": 3}}`)
	log.Println("Connect to /sse to receive SSE events.")

	// Start the server.
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
