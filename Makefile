.PHONY: build clean

PLUGIN_NAME := oido-gcalendar
BINARY := $(PLUGIN_NAME)-mcp

build:
	@echo "Building $(PLUGIN_NAME) MCP server..."
	CGO_ENABLED=0 go build -o $(BINARY) .
	@echo "✓ Built: $(BINARY)"

clean:
	rm -f $(BINARY)
