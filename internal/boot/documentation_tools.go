package boot

import (
	"reasonix/internal/instruction"
	"reasonix/internal/productdocs"
	"reasonix/internal/tool"
)

func registerDocumentationTools(registry *tool.Registry, documents []instruction.Document) {
	registry.Add(productdocs.NewTool())
	registry.Add(instruction.NewSourcesTool(documents))
}
