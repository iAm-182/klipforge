package web

import "embed"

//go:embed index.html app.css app.js favicon.ico favicon.png apple-touch-icon.png
var FS embed.FS
