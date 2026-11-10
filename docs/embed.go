package docs

import "embed"

//go:embed openapi.json index.html swagger-init.js
var Files embed.FS
