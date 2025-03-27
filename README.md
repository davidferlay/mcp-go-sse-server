# mcp-go-sse-server

A simple MCP server exposed via SSE with examples tools, resources and prompts.
Modified example from https://github.com/mark3labs/mcp-go/blob/main/examples/everything/main.go
Run with:
- `go build . && ./mcp-go-sse-server --transport sse --baseurl http://localhost`
- `go build . && ./mcp-go-sse-server --transport sse --baseurl https://mcp.example.com --omitPort`

A real use-case example implementing tools to read from a postgres database and write to a nats channel can be found on branch [`nats-postgres`](https://github.com/davidferlay/mcp-go-sse-server/tree/nats-postgres)
