package web

import "embed"

//go:embed index.html app.css b1_theme.css app.js b1_dashboard.js cutter_helpers.js vendor/sortable.min.js favicon.ico favicon.png apple-touch-icon.png
var FS embed.FS
