# mcp-golang-http-server

A simple MCP server exposed via SSE with examples tools, resources and prompts.
Modified example from https://github.com/mark3labs/mcp-go/blob/main/examples/everything/main.go
Run with:
- `go build . && ./mcp-golang-http-server --transport sse --baseurl http://localhost`
- `go build . && ./mcp-golang-http-server --transport sse --baseurl https://mcp.example.com --omitPort`
