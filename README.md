# mcp-go-sse-server

A simple MCP server exposed via SSE with tools to read from a postgres database and write to nats.
Modified example from https://github.com/mark3labs/mcp-go/blob/main/examples/everything/main.go
Run with:
- `go build . && ./mcp-go-sse-server --transport sse --baseurl http://localhost -pgdsn postgresql://localhost:5432/ssot --natsurl nats://localhost:6222`
- `go build . && ./mcp-go-sse-server --transport sse --baseurl https://mcp.example.com --omitPort -pgdsn postgresql://localhost:5432/ssot --natsurl nats://localhost:6222`
